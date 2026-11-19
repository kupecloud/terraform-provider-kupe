package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func appTestEnvironmentModel() AppEnvironmentResourceModel {
	return AppEnvironmentResourceModel{
		App: types.StringValue("shop"), Name: types.StringValue("production"), Cluster: types.StringValue("europe"),
		DeletionProtection: types.BoolValue(true), DeploymentRevision: types.Int64Value(1),
		Runtime:        appRuntimeToObject(&client.AppRuntime{Image: "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("a", 64), Port: 8080, Replicas: 1, Resources: client.AppResources{Requests: client.AppQuantities{CPU: "250m", Memory: "256Mi"}, Limits: client.AppQuantities{CPU: "1", Memory: "512Mi"}}, HealthChecks: client.AppHealthChecks{Readiness: &client.AppProbe{TCP: &client.AppTCPProbe{}}}}),
		Secrets:        types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, []attr.Value{}),
		Databases:      types.ListValueMust(types.ObjectType{AttrTypes: appDatabaseTypes}, []attr.Value{}),
		DatabaseStatus: types.ListValueMust(types.ObjectType{AttrTypes: appDatabaseStatusTypes}, []attr.Value{}),
		Domains:        types.SetValueMust(types.StringType, []attr.Value{}),
	}
}

func TestAppImageDigestRejectsTagsAndAllowsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   types.String
		invalid bool
	}{
		{"tag", types.StringValue("ghcr.io/kupecloud/hello-kupe:latest"), true},
		{"unqualified", types.StringValue("hello-kupe@sha256:" + strings.Repeat("a", 64)), true},
		{"digest", types.StringValue("ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("a", 64)), false},
		{"unknown", types.StringUnknown(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp validator.StringResponse
			appImageDigest{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("runtime").AtName("image"), ConfigValue: tc.value}, &resp)
			if resp.Diagnostics.HasError() != tc.invalid {
				t.Fatalf("digest validation: %v", resp.Diagnostics)
			}
			if tc.invalid && !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "data.kupe_app_image") {
				t.Fatal("missing image-resolution hint")
			}
		})
	}
}

func TestAppEnvironmentPatchAlwaysIncludesCompletePlannedLists(t *testing.T) {
	prior := appTestEnvironmentModel()
	plan := prior
	plan.Domains = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("shop.example.com")})
	patch, changed, diags := appEnvironmentPatch(context.Background(), &plan, &prior)
	if diags.HasError() || !changed {
		t.Fatalf("patch failed: %v", diags)
	}
	if patch.Runtime == nil || patch.Runtime.Env == nil || patch.Secrets == nil || *patch.Secrets == nil || patch.Domains == nil || len(*patch.Domains) != 1 {
		t.Fatalf("incomplete planned replacement lists: %+v", patch)
	}
	if appEnvironmentDeployingChange(&plan, &prior) {
		t.Fatal("domain-only change was classified as a deployment")
	}
}

func TestAppEnvironmentOnlyReferencedSecretChangesDeploy(t *testing.T) {
	prior := appTestEnvironmentModel()
	secret := func(name, vaultPath string) attr.Value {
		return types.ObjectValueMust(appSecretTypes, map[string]attr.Value{"name": types.StringValue(name), "path": types.StringValue(vaultPath)})
	}
	prior.Secrets = types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, []attr.Value{secret("payments", "apps/old"), secret("unused", "apps/unused")})
	runtime, _, _, _, diags := appEnvironmentSettings(context.Background(), &prior)
	if diags.HasError() {
		t.Fatal(diags)
	}
	runtime.Env = []client.AppEnvVar{{Name: "API_KEY", ValueFrom: &client.AppValueFrom{SecretKeyRef: client.AppSecretKeyRef{Secret: "payments", Key: "API_KEY"}}}}
	prior.Runtime = appRuntimeToObject(runtime)
	plan := prior
	plan.Secrets = types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, []attr.Value{secret("payments", "apps/old"), secret("unused", "apps/changed")})
	if appEnvironmentDeployingChange(&plan, &prior) {
		t.Fatal("unused secret path requested a deployment")
	}
	plan.Secrets = types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, []attr.Value{secret("payments", "apps/new"), secret("unused", "apps/unused")})
	if !appEnvironmentDeployingChange(&plan, &prior) {
		t.Fatal("referenced secret path did not request a deployment")
	}
}

func TestAppEnvironmentDeploymentRevisionPlanBoundaries(t *testing.T) {
	ctx := context.Background()
	r := &AppEnvironmentResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	for _, tc := range []struct {
		name           string
		prior, planned int64
		invalid        bool
	}{
		{"unchanged", 2, 2, false}, {"one retry", 2, 3, false}, {"skip", 2, 4, true}, {"lower", 2, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := testPlanValue(t, sch, map[string]tftypes.Value{"deployment_revision": tftypes.NewValue(tftypes.Number, tc.prior)})
			planned := testPlanValue(t, sch, map[string]tftypes.Value{"deployment_revision": tftypes.NewValue(tftypes.Number, tc.planned)})
			req := resource.ModifyPlanRequest{State: tfsdk.State{Schema: sch.Schema, Raw: prior}, Plan: tfsdk.Plan{Schema: sch.Schema, Raw: planned}}
			resp := resource.ModifyPlanResponse{Plan: req.Plan}
			r.ModifyPlan(ctx, req, &resp)
			if resp.Diagnostics.HasError() != tc.invalid {
				t.Fatalf("revision plan: %v", resp.Diagnostics)
			}
		})
	}
}

func TestAppEnvironmentConfigurationValidatesCrossFieldLimits(t *testing.T) {
	ctx := context.Background()
	r := &AppEnvironmentResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	base := appTestEnvironmentModel()
	for _, tc := range []struct {
		name   string
		change func(*client.AppRuntime)
		at     string // path of the expected error; empty when valid
	}{
		{"defaults", func(*client.AppRuntime) {}, ""},
		{"two value sources", func(w *client.AppRuntime) {
			value := "literal"
			w.Env = []client.AppEnvVar{{Name: "KEY", Value: &value, ValueFrom: &client.AppValueFrom{SecretKeyRef: client.AppSecretKeyRef{Secret: "absent", Key: "KEY"}}}}
		}, "runtime.env[0]"},
		{"long multibyte argument", func(w *client.AppRuntime) { w.Args = []string{strings.Repeat("é", 513)} }, "runtime.args[0]"},
		{"both probes", func(w *client.AppRuntime) { w.HealthChecks.Readiness.HTTP = &client.AppHTTPProbe{Path: "/"} }, "runtime.health_checks.readiness"},
		{"limit below request", func(w *client.AppRuntime) { w.Resources.Limits.CPU = "100m" }, "runtime.resources.limits.cpu"},
		{"autoscaling pod range", func(w *client.AppRuntime) {
			w.Autoscaling = &client.AppAutoscaling{MinPods: 2, MaxPods: 6, CPUPercent: 70}
		}, ""},
		{"autoscaling one pod", func(w *client.AppRuntime) {
			w.Autoscaling = &client.AppAutoscaling{MinPods: 3, MaxPods: 3, CPUPercent: 70}
		}, ""},
		{"autoscaling min above max", func(w *client.AppRuntime) {
			w.Autoscaling = &client.AppAutoscaling{MinPods: 6, MaxPods: 2, CPUPercent: 70}
		}, "runtime.autoscaling.min_pods"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime, _, _, _, diags := appEnvironmentSettings(ctx, &base)
			if diags.HasError() {
				t.Fatal(diags)
			}
			tc.change(runtime)
			runtimeRaw, err := appRuntimeToObject(runtime).ToTerraformValue(ctx)
			if err != nil {
				t.Fatal(err)
			}
			secretsRaw, err := base.Secrets.ToTerraformValue(ctx)
			if err != nil {
				t.Fatal(err)
			}
			domainsRaw, err := base.Domains.ToTerraformValue(ctx)
			if err != nil {
				t.Fatal(err)
			}
			raw := testPlanValue(t, sch, map[string]tftypes.Value{"runtime": runtimeRaw, "secrets": secretsRaw, "domains": domainsRaw})
			var resp resource.ValidateConfigResponse
			r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sch.Schema, Raw: raw}}, &resp)
			if tc.at == "" {
				if resp.Diagnostics.HasError() {
					t.Fatalf("valid configuration refused: %v", resp.Diagnostics)
				}
				return
			}
			var paths []string
			for _, got := range resp.Diagnostics.Errors() {
				if withPath, ok := got.(diag.DiagnosticWithPath); ok {
					paths = append(paths, withPath.Path().String())
				}
			}
			if !slices.Contains(paths, tc.at) {
				t.Fatalf("error paths = %v, want %s: %v", paths, tc.at, resp.Diagnostics)
			}
		})
	}
}

func TestAppReleaseMatchesCanonicalQuantitiesAndReferencedKeys(t *testing.T) {
	m := appTestEnvironmentModel()
	runtime, _, _, _, diags := appEnvironmentSettings(context.Background(), &m)
	if diags.HasError() {
		t.Fatal(diags)
	}
	runtime.Resources.Requests.CPU = "0.5"
	env := &client.AppEnvironment{DeploymentRevision: 2, Runtime: runtime}
	release := &client.AppRelease{DeploymentRevision: 2, Runtime: client.AppReleaseRuntime{AppRuntime: *runtime}}
	release.Runtime.Resources.Requests.CPU = "500m"
	if !appReleaseMatches(env, release) {
		t.Fatal("equal quantity spellings failed request correlation")
	}
	release.DeploymentRevision = 1
	if appReleaseMatches(env, release) {
		t.Fatal("old revision satisfied retry")
	}
}

// F2 uses real null object values. Resource ValidateConfig runs before the
// framework's nested schema checks, so these must diagnose rather than panic.
func TestAppEnvironmentNullListEntriesDiagnoseAndUnknownDefers(t *testing.T) {
	ctx := context.Background()
	r := NewAppEnvironmentResource().(*AppEnvironmentResource)
	sch := testResourceSchema(t, r)
	for _, listName := range []string{"secrets", "env"} {
		for _, unknown := range []bool{false, true} {
			t.Run(listName+fmt.Sprint(unknown), func(t *testing.T) {
				m := appTestEnvironmentModel()
				attrs := appSecretTypes
				if listName == "env" {
					attrs = appEnvTypes
				}
				item := types.ObjectNull(attrs)
				if unknown {
					item = types.ObjectUnknown(attrs)
				}
				list := types.ListValueMust(types.ObjectType{AttrTypes: attrs}, []attr.Value{item})
				if listName == "secrets" {
					m.Secrets = list
				} else {
					runtime := m.Runtime.Attributes()
					runtime["env"] = list
					m.Runtime = types.ObjectValueMust(appRuntimeTypes, runtime)
				}
				rawAttrs := map[string]tftypes.Value{}
				for name, value := range map[string]attr.Value{"runtime": m.Runtime, "secrets": m.Secrets, "domains": m.Domains} {
					raw, err := value.ToTerraformValue(ctx)
					if err != nil {
						t.Fatal(err)
					}
					rawAttrs[name] = raw
				}
				raw := testPlanValue(t, sch, rawAttrs)
				var resp resource.ValidateConfigResponse
				r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sch.Schema, Raw: raw}}, &resp)
				if resp.Diagnostics.HasError() == unknown {
					t.Fatalf("null/unknown validation diagnostics: %v", resp.Diagnostics)
				}
				if !unknown {
					if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "non-null objects") {
						t.Fatalf("missing null-entry detail: %v", resp.Diagnostics)
					}
					attribute, ok := resp.Diagnostics.Errors()[0].(diag.DiagnosticWithPath)
					if !ok || !strings.Contains(attribute.Path().String(), "[0]") {
						t.Fatalf("missing list-index path: %v", resp.Diagnostics)
					}
					if _, _, _, _, d := appEnvironmentSettings(ctx, &m); !d.HasError() {
						t.Fatal("apply conversion accepted a null object entry")
					}
				}
				// Force secret-difference traversal, including the null/unknown entry.
				prior := appTestEnvironmentModel()
				prior.Secrets = types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, []attr.Value{types.ObjectValueMust(appSecretTypes, map[string]attr.Value{"name": types.StringValue("unused"), "path": types.StringValue("apps/unused")})})
				// Keep Runtime and revision equal so secret-difference analysis reaches
				// the env element guard instead of returning on a changed Runtime.
				prior.Runtime = m.Runtime
				prior.DeploymentRevision = m.DeploymentRevision
				if !appEnvironmentDeployingChange(&m, &prior) {
					t.Fatal("null or unknown nested entries must conservatively require deployment analysis")
				}
			})
		}
	}
}

func appTestDatabase(name, database, logical, env string) attr.Value {
	optional := func(value string) types.String {
		if value == "" {
			return types.StringNull()
		}
		return types.StringValue(value)
	}
	return types.ObjectValueMust(appDatabaseTypes, map[string]attr.Value{"name": types.StringValue(name), "database": types.StringValue(database), "logical_database": optional(logical), "env": optional(env)})
}

func appTestDatabases(values ...attr.Value) types.List {
	return types.ListValueMust(types.ObjectType{AttrTypes: appDatabaseTypes}, values)
}

func TestAppEnvironmentConfigurationValidatesDatabaseConnections(t *testing.T) {
	ctx := context.Background()
	r := &AppEnvironmentResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	base := appTestEnvironmentModel()
	runtime, _, _, _, diags := appEnvironmentSettings(ctx, &base)
	if diags.HasError() {
		t.Fatal(diags)
	}
	literal := "info"
	runtime.Env = []client.AppEnvVar{{Name: "LOG_LEVEL", Value: &literal}}
	for _, tc := range []struct {
		name, app           string
		databases           types.List
		summary, detail, at string
	}{
		{name: "two connections to one database", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", ""), appTestDatabase("reports", "orders-db", "analytics", "REPORTS_URL"))},
		{name: "duplicate name", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", ""), appTestDatabase("main", "users-db", "", "USERS_URL")), summary: "Duplicate connection", detail: "Connection names must be unique.", at: "databases[1].name"},
		{name: "default env used twice", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", ""), appTestDatabase("users", "users-db", "", "DATABASE_URL")), summary: "Duplicate variable", detail: "Another connection already sets this variable. Give each connection its own env.", at: "databases[1].env"},
		{name: "env set in runtime.env", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", "LOG_LEVEL")), summary: "Variable already set", detail: "runtime.env already sets this variable. Choose another env for this connection.", at: "databases[0].env"},
		{name: "same database twice", databases: appTestDatabases(appTestDatabase("main", "orders-db", "analytics", ""), appTestDatabase("again", "orders-db", "analytics", "AGAIN_URL")), summary: "Duplicate database", detail: "Another connection already uses this database and logical_database. Connect each one once per environment.", at: "databases[1].database"},
		{name: "binding name too long", app: strings.Repeat("a", 32), databases: appTestDatabases(appTestDatabase(strings.Repeat("c", 8), "orders-db", "", "")), summary: "Connection name too long", detail: "The app, environment and connection names together can be at most 49 characters, so that the connection's database binding name, app-<app>-<environment>-<connection>-migrate, fits in 63.", at: "databases[0].name"},
		{name: "binding name at the limit", app: strings.Repeat("a", 32), databases: appTestDatabases(appTestDatabase(strings.Repeat("c", 7), "orders-db", "", ""))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app
			if app == "" {
				app = "shop"
			}
			rawAttrs := map[string]tftypes.Value{"app": tftypes.NewValue(tftypes.String, app), "name": tftypes.NewValue(tftypes.String, "production")}
			for name, value := range map[string]attr.Value{"runtime": appRuntimeToObject(runtime), "secrets": base.Secrets, "domains": base.Domains, "databases": tc.databases} {
				raw, err := value.ToTerraformValue(ctx)
				if err != nil {
					t.Fatal(err)
				}
				rawAttrs[name] = raw
			}
			var resp resource.ValidateConfigResponse
			r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sch.Schema, Raw: testPlanValue(t, sch, rawAttrs)}}, &resp)
			if tc.summary == "" {
				if resp.Diagnostics.HasError() {
					t.Fatalf("valid connections refused: %v", resp.Diagnostics)
				}
				return
			}
			if got := resp.Diagnostics.ErrorsCount(); got != 1 {
				t.Fatalf("errors = %d, want 1: %v", got, resp.Diagnostics)
			}
			got := resp.Diagnostics.Errors()[0]
			if got.Summary() != tc.summary || got.Detail() != tc.detail {
				t.Fatalf("diagnostic = %q / %q, want %q / %q", got.Summary(), got.Detail(), tc.summary, tc.detail)
			}
			if withPath, ok := got.(diag.DiagnosticWithPath); !ok || withPath.Path().String() != tc.at {
				t.Fatalf("diagnostic path = %v, want %s", got, tc.at)
			}
		})
	}
}

func TestAppEnvironmentDatabaseChangesDeployAndPatchTheWholeList(t *testing.T) {
	prior := appTestEnvironmentModel()
	prior.Databases = appTestDatabases(appTestDatabase("main", "orders-db", "", "DATABASE_URL"))
	for _, tc := range []struct {
		name string
		list types.List
		want int
	}{
		{"env only", appTestDatabases(appTestDatabase("main", "orders-db", "", "ORDERS_URL")), 1},
		{"added", appTestDatabases(appTestDatabase("main", "orders-db", "", "DATABASE_URL"), appTestDatabase("reports", "orders-db", "analytics", "REPORTS_URL")), 2},
		{"all removed", appTestDatabases(), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := prior
			plan.Databases = tc.list
			if !appEnvironmentDeployingChange(&plan, &prior) {
				t.Fatal("a databases change was not classified as a deployment")
			}
			patch, changed, diags := appEnvironmentPatch(context.Background(), &plan, &prior)
			if diags.HasError() || !changed {
				t.Fatalf("patch failed: %v", diags)
			}
			if patch.Databases == nil || len(*patch.Databases) != tc.want || patch.Runtime == nil || patch.Secrets == nil || patch.Domains == nil {
				t.Fatalf("patch did not carry the complete lists: %+v", patch)
			}
		})
	}
	unchanged := prior
	if appEnvironmentDeployingChange(&unchanged, &prior) {
		t.Fatal("unchanged connections were classified as a deployment")
	}
}

func TestAppEnvironmentConnectionDatabaseIsImmutable(t *testing.T) {
	prior := appTestDatabases(appTestDatabase("main", "orders-db", "", "DATABASE_URL"), appTestDatabase("reports", "orders-db", "analytics", "REPORTS_URL"))
	for _, tc := range []struct {
		name    string
		planned types.List
		detail  string
	}{
		{"unchanged", prior, ""},
		{"env and order change", appTestDatabases(appTestDatabase("reports", "orders-db", "analytics", "STATS_URL"), appTestDatabase("main", "orders-db", "", "DATABASE_URL")), ""},
		{"removed and new name", appTestDatabases(appTestDatabase("primary", "users-db", "", "DATABASE_URL")), ""},
		{"database changed", appTestDatabases(appTestDatabase("main", "users-db", "", "DATABASE_URL")), `Connection "main" already uses database "orders-db". Remove the connection and apply, then add it again with the new database.`},
		{"logical database changed", appTestDatabases(appTestDatabase("main", "orders-db", "", "DATABASE_URL"), appTestDatabase("reports", "orders-db", "sales", "REPORTS_URL")), `Connection "reports" already uses database "orders-db/analytics". Remove the connection and apply, then add it again with the new database.`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			appDatabasesImmutable(tc.planned, prior, &diags)
			if tc.detail == "" {
				if diags.HasError() {
					t.Fatalf("allowed change refused: %v", diags)
				}
				return
			}
			if diags.ErrorsCount() != 1 || diags.Errors()[0].Summary() != "Connection database cannot change" || diags.Errors()[0].Detail() != tc.detail {
				t.Fatalf("diagnostics = %v, want %q", diags, tc.detail)
			}
		})
	}
}

func TestAppEnvironmentModifyPlanRefusesDatabaseChangeOnlyInPlace(t *testing.T) {
	ctx := context.Background()
	r := &AppEnvironmentResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	raw := func(cluster string, databases types.List) tftypes.Value {
		value, err := databases.ToTerraformValue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return testPlanValue(t, sch, map[string]tftypes.Value{"app": tftypes.NewValue(tftypes.String, "shop"), "name": tftypes.NewValue(tftypes.String, "production"), "cluster": tftypes.NewValue(tftypes.String, cluster), "deployment_revision": tftypes.NewValue(tftypes.Number, 1), "databases": value})
	}
	prior := raw("europe", appTestDatabases(appTestDatabase("main", "orders-db", "", "DATABASE_URL")))
	changed := appTestDatabases(appTestDatabase("main", "users-db", "", "DATABASE_URL"))
	for _, tc := range []struct {
		name, cluster string
		invalid       bool
	}{{"in place", "europe", true}, {"replacement", "america", false}} {
		t.Run(tc.name, func(t *testing.T) {
			req := resource.ModifyPlanRequest{State: tfsdk.State{Schema: sch.Schema, Raw: prior}, Plan: tfsdk.Plan{Schema: sch.Schema, Raw: raw(tc.cluster, changed)}}
			resp := resource.ModifyPlanResponse{Plan: req.Plan}
			r.ModifyPlan(ctx, req, &resp)
			if resp.Diagnostics.HasError() != tc.invalid {
				t.Fatalf("diagnostics: %v", resp.Diagnostics)
			}
			if tc.invalid {
				withPath, ok := resp.Diagnostics.Errors()[0].(diag.DiagnosticWithPath)
				if !ok || withPath.Path().String() != "databases[0]" {
					t.Fatalf("diagnostic not on the connection: %v", resp.Diagnostics)
				}
			}
		})
	}
}

func TestAppEnvironmentStateMapsDatabasesAndOmitsSecretNames(t *testing.T) {
	var m AppEnvironmentResourceModel
	env := &client.AppEnvironment{App: "shop", Environment: "production", Databases: []client.AppDatabase{{Name: "main", Database: "orders-db", Env: "DATABASE_URL"}, {Name: "reports", Database: "orders-db", LogicalDatabase: "analytics", Env: "REPORTS_URL"}},
		Status: &client.AppEnvironmentStatus{Databases: []client.AppDatabaseStatus{{Name: "main", Env: "DATABASE_URL", Phase: "Pending", Message: "waiting for database orders-db", RuntimeSecret: "main-app", MigrationSecret: "main-migrate"}}}}
	mapAppEnvironmentToState(env, "", &m)
	want := appTestDatabases(appTestDatabase("main", "orders-db", "", "DATABASE_URL"), appTestDatabase("reports", "orders-db", "analytics", "REPORTS_URL"))
	if !m.Databases.Equal(want) {
		t.Fatalf("databases = %v, want %v", m.Databases, want)
	}
	wantStatus := types.ListValueMust(types.ObjectType{AttrTypes: appDatabaseStatusTypes}, []attr.Value{types.ObjectValueMust(appDatabaseStatusTypes, map[string]attr.Value{"name": types.StringValue("main"), "phase": types.StringValue("Pending"), "message": types.StringValue("waiting for database orders-db")})})
	if !m.DatabaseStatus.Equal(wantStatus) {
		t.Fatalf("database_status = %v, want %v", m.DatabaseStatus, wantStatus)
	}
	// A response without connections maps to the empty lists the schema
	// defaults to.
	mapAppEnvironmentToState(&client.AppEnvironment{}, "", &m)
	if m.Databases.IsNull() || len(m.Databases.Elements()) != 0 || m.DatabaseStatus.IsNull() || len(m.DatabaseStatus.Elements()) != 0 {
		t.Fatalf("absent connections mapped to %v / %v, want empty lists", m.Databases, m.DatabaseStatus)
	}
}

func TestAppReleaseMatchesDatabasesByName(t *testing.T) {
	m := appTestEnvironmentModel()
	runtime, _, _, _, diags := appEnvironmentSettings(context.Background(), &m)
	if diags.HasError() {
		t.Fatal(diags)
	}
	main, reports := client.AppDatabase{Name: "main", Database: "orders-db", Env: "DATABASE_URL"}, client.AppDatabase{Name: "reports", Database: "orders-db", LogicalDatabase: "analytics", Env: "REPORTS_URL"}
	env := &client.AppEnvironment{DeploymentRevision: 1, Runtime: runtime, Databases: []client.AppDatabase{main, reports}}
	release := &client.AppRelease{DeploymentRevision: 1, Runtime: client.AppReleaseRuntime{AppRuntime: *runtime, Databases: []client.AppDatabase{reports, main}}}
	if !appReleaseMatches(env, release) {
		t.Fatal("the same connections in another order did not match")
	}
	release.Runtime.Databases = []client.AppDatabase{main}
	if appReleaseMatches(env, release) {
		t.Fatal("a release without the new connection matched")
	}
	moved := reports
	moved.Env = "STATS_URL"
	release.Runtime.Databases = []client.AppDatabase{main, moved}
	if appReleaseMatches(env, release) {
		t.Fatal("a release with the old env matched")
	}
}

// The immutability check skips replacements by comparing
// appEnvironmentReplaceAttributes; a new RequiresReplace attribute must be
// added there too.
func TestAppEnvironmentReplaceAttributesMatchSchema(t *testing.T) {
	ctx := context.Background()
	sch := testResourceSchema(t, NewAppEnvironmentResource())
	replace := stringplanmodifier.RequiresReplace().Description(ctx)
	var got []string
	for name, attribute := range sch.Schema.Attributes {
		var descriptions []string
		switch a := attribute.(type) {
		case schema.StringAttribute:
			for _, m := range a.PlanModifiers {
				descriptions = append(descriptions, m.Description(ctx))
			}
		case schema.Int64Attribute:
			for _, m := range a.PlanModifiers {
				descriptions = append(descriptions, m.Description(ctx))
			}
		case schema.BoolAttribute:
			for _, m := range a.PlanModifiers {
				descriptions = append(descriptions, m.Description(ctx))
			}
		case schema.ListNestedAttribute:
			for _, m := range a.PlanModifiers {
				descriptions = append(descriptions, m.Description(ctx))
			}
		case schema.SetAttribute:
			for _, m := range a.PlanModifiers {
				descriptions = append(descriptions, m.Description(ctx))
			}
		case schema.SingleNestedAttribute:
			for _, m := range a.PlanModifiers {
				descriptions = append(descriptions, m.Description(ctx))
			}
		}
		for _, description := range descriptions {
			if description == replace {
				got = append(got, name)
			}
		}
	}
	sort.Strings(got)
	want := append([]string{}, appEnvironmentReplaceAttributes...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiresReplace attributes = %v, appEnvironmentReplaceAttributes = %v", got, want)
	}
}

func appTestAutoscalingModel(replicas, minPods, maxPods int32) AppEnvironmentResourceModel {
	m := appTestEnvironmentModel()
	runtime, _, _, _, diags := appEnvironmentSettings(context.Background(), &m)
	if diags.HasError() {
		panic(fmt.Sprint(diags))
	}
	runtime.Replicas = replicas
	if minPods > 0 {
		runtime.Autoscaling = &client.AppAutoscaling{MinPods: minPods, MaxPods: maxPods, CPUPercent: 70}
	}
	m.Runtime = appRuntimeToObject(runtime)
	return m
}

// The block reaches the API as autoscaling, and its removal as an explicit
// null that the API's merge patch reads as "stop autoscaling".
func TestAppEnvironmentAutoscalingWire(t *testing.T) {
	ctx := context.Background()
	scaled := appTestAutoscalingModel(1, 2, 6)
	runtime, _, _, _, diags := appEnvironmentSettings(ctx, &scaled)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if want := (client.AppAutoscaling{MinPods: 2, MaxPods: 6, CPUPercent: 70}); runtime.Autoscaling == nil || *runtime.Autoscaling != want {
		t.Fatalf("autoscaling = %+v, want %+v", runtime.Autoscaling, want)
	}
	plain := appTestAutoscalingModel(1, 0, 0)
	patch, changed, diags := appEnvironmentPatch(ctx, &plain, &scaled)
	if diags.HasError() || !changed || patch.Runtime == nil || patch.Runtime.Autoscaling != nil {
		t.Fatalf("removal patch = %+v, changed %v, %v", patch.Runtime, changed, diags)
	}
	data, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"autoscaling":null`) {
		t.Fatalf(`removal patch %s does not send "autoscaling":null`, data)
	}
}

func TestAppEnvironmentAutoscalingState(t *testing.T) {
	if got := appRuntimeToObject(&client.AppRuntime{}).Attributes()["autoscaling"]; !got.IsNull() {
		t.Fatalf("a response without autoscaling maps to %v, want null", got)
	}
	got := appRuntimeToObject(&client.AppRuntime{Autoscaling: &client.AppAutoscaling{MinPods: 2, MaxPods: 6}}).Attributes()["autoscaling"].(types.Object).Attributes()
	if got["min_pods"].(types.Int64).ValueInt64() != 2 || got["max_pods"].(types.Int64).ValueInt64() != 6 || got["cpu_percent"].(types.Int64).ValueInt64() != appDefaultCPUPercent {
		t.Fatalf("autoscaling without cpuPercent maps to %v, want the default target", got)
	}
}

// A release records min_pods as its replicas under autoscaling, so only a
// change to what a release records deploys.
func TestAppEnvironmentAutoscalingDeployingChange(t *testing.T) {
	for _, tc := range []struct {
		name          string
		prior, plan   AppEnvironmentResourceModel
		wantDeploying bool
	}{
		{"replicas under autoscaling", appTestAutoscalingModel(1, 2, 6), appTestAutoscalingModel(3, 2, 6), false},
		{"replicas without autoscaling", appTestAutoscalingModel(1, 0, 0), appTestAutoscalingModel(3, 0, 0), true},
		{"max_pods", appTestAutoscalingModel(1, 2, 6), appTestAutoscalingModel(1, 2, 8), true},
		{"min_pods", appTestAutoscalingModel(1, 2, 6), appTestAutoscalingModel(1, 3, 6), true},
		{"block added", appTestAutoscalingModel(2, 0, 0), appTestAutoscalingModel(2, 2, 6), true},
		{"block removed", appTestAutoscalingModel(2, 2, 6), appTestAutoscalingModel(2, 0, 0), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := appEnvironmentDeployingChange(&tc.plan, &tc.prior); got != tc.wantDeploying {
				t.Fatalf("deploying = %v, want %v", got, tc.wantDeploying)
			}
		})
	}
	unknown := appTestAutoscalingModel(1, 2, 6)
	attrs := unknown.Runtime.Attributes()
	scaling := attrs["autoscaling"].(types.Object).Attributes()
	scaling["min_pods"] = types.Int64Unknown()
	attrs["autoscaling"] = types.ObjectValueMust(appAutoscalingTypes, scaling)
	unknown.Runtime = types.ObjectValueMust(appRuntimeTypes, attrs)
	prior := appTestAutoscalingModel(1, 2, 6)
	if !appEnvironmentDeployingChange(&unknown, &prior) {
		t.Fatal("an unknown min_pods was not treated as deploying")
	}
}

func TestAppReleaseMatchesAutoscalingReplicas(t *testing.T) {
	m := appTestAutoscalingModel(1, 2, 6)
	runtime, _, _, _, diags := appEnvironmentSettings(context.Background(), &m)
	if diags.HasError() {
		t.Fatal(diags)
	}
	env := &client.AppEnvironment{DeploymentRevision: 1, Runtime: runtime}
	release := &client.AppRelease{DeploymentRevision: 1, Runtime: client.AppReleaseRuntime{AppRuntime: *runtime}}
	release.Runtime.Replicas = 2
	release.Runtime.Autoscaling = &client.AppAutoscaling{MinPods: 2, MaxPods: 6, CPUPercent: 70}
	if !appReleaseMatches(env, release) {
		t.Fatal("a release recording min_pods as replicas did not match")
	}
	release.Runtime.Autoscaling = &client.AppAutoscaling{MinPods: 2, MaxPods: 5, CPUPercent: 70}
	if appReleaseMatches(env, release) {
		t.Fatal("a release with other autoscaling settings matched")
	}
	release.Runtime.Autoscaling = nil
	if appReleaseMatches(env, release) {
		t.Fatal("a release without autoscaling matched an autoscaling environment")
	}
}

// A state written before autoscaling existed has no runtime.autoscaling key; it
// decodes with the block null, so it plans no change against a configuration
// without the block.
func TestAppEnvironmentStateWithoutAutoscalingDecodes(t *testing.T) {
	ctx := context.Background()
	r := NewAppEnvironmentResource().(*AppEnvironmentResource)
	sch := testResourceSchema(t, r)
	m := appTestEnvironmentModel()
	runtimeRaw, err := m.Runtime.ToTerraformValue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored := testPlanValue(t, sch, map[string]tftypes.Value{"runtime": runtimeRaw})
	state := map[string]any{}
	if err := json.Unmarshal(testTerraformJSON(t, stored), &state); err != nil {
		t.Fatal(err)
	}
	delete(state["runtime"].(map[string]any), "autoscaling")
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{TypeName: "kupe_app_environment", RawState: &tfprotov6.RawState{JSON: data}})
	if err != nil || len(resp.Diagnostics) > 0 {
		t.Fatalf("upgrade state: %v %v", err, resp.Diagnostics)
	}
	upgraded, err := resp.UpgradedState.Unmarshal(sch.Schema.Type().TerraformType(ctx))
	if err != nil {
		t.Fatal(err)
	}
	var decoded AppEnvironmentResourceModel
	if diags := (tfsdk.State{Schema: sch.Schema, Raw: upgraded}).Get(ctx, &decoded); diags.HasError() {
		t.Fatal(diags)
	}
	if !decoded.Runtime.Equal(m.Runtime) {
		t.Fatalf("runtime = %v, want %v with autoscaling null", decoded.Runtime, m.Runtime)
	}
}

// testTerraformJSON encodes a known value as Terraform's JSON state encoding.
func testTerraformJSON(t *testing.T, value tftypes.Value) []byte {
	t.Helper()
	var encode func(tftypes.Value) any
	encode = func(v tftypes.Value) any {
		if v.IsNull() || !v.IsKnown() {
			return nil
		}
		switch typ := v.Type(); {
		case typ.Is(tftypes.Object{}):
			var attrs map[string]tftypes.Value
			if err := v.As(&attrs); err != nil {
				t.Fatal(err)
			}
			out := map[string]any{}
			for name, attr := range attrs {
				out[name] = encode(attr)
			}
			return out
		case typ.Is(tftypes.List{}), typ.Is(tftypes.Set{}):
			var items []tftypes.Value
			if err := v.As(&items); err != nil {
				t.Fatal(err)
			}
			out := []any{}
			for _, item := range items {
				out = append(out, encode(item))
			}
			return out
		case typ.Is(tftypes.Number):
			var number big.Float
			if err := v.As(&number); err != nil {
				t.Fatal(err)
			}
			return json.Number(number.Text('f', -1))
		case typ.Is(tftypes.Bool):
			var b bool
			if err := v.As(&b); err != nil {
				t.Fatal(err)
			}
			return b
		default:
			var text string
			if err := v.As(&text); err != nil {
				t.Fatal(err)
			}
			return text
		}
	}
	data, err := json.Marshal(encode(value))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestAppEnvironmentDeleteDiagnosticsNameVaultEntry pins what the refusal and
// the still-terminating warning say happens to the environment's own Vault
// entry, the cluster and the other Vault entries.
func TestAppEnvironmentDeleteDiagnosticsNameVaultEntry(t *testing.T) {
	m := &AppEnvironmentResourceModel{App: types.StringValue("shop"), Name: types.StringValue("production")}
	entry := "Its own Vault entry `apps/shop/production`, where its secrets are stored by default, "
	rule := " permanently deleted unless a secret or another environment outside this one still uses it. The cluster and other Vault entries are kept."
	refusal := fmt.Errorf("delete app environment: %w",
		&client.APIError{StatusCode: http.StatusConflict, Code: client.CodeDeletionProtected, Message: "deletion protection is on"})
	summary, detail, ok := appEnvironmentKind.deleteRefused(m, refusal)
	if !ok || summary != "app environment has deletion protection enabled" {
		t.Fatalf("deleteRefused() = %q, %v", summary, ok)
	}
	if !strings.HasSuffix(detail, "Destroying an environment deletes its namespace and everything in it. "+entry+"is"+rule) {
		t.Errorf("refusal detail = %q", detail)
	}
	if got := appEnvironmentKind.stillDeleting(m); !strings.HasSuffix(got, "Its name stays reserved until cleanup finishes. "+entry+"will be"+rule) {
		t.Errorf("stillDeleting = %q", got)
	}
}
