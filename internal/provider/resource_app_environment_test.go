package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func appTestEnvironmentModel() AppEnvironmentResourceModel {
	return AppEnvironmentResourceModel{
		App: types.StringValue("shop"), Name: types.StringValue("production"), Cluster: types.StringValue("europe"),
		DeletionProtection: types.BoolValue(true), DeploymentRevision: types.Int64Value(1),
		Web:            appWebToObject(&client.AppWeb{Image: "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("a", 64), Port: 8080, Replicas: 1, Resources: client.AppResources{Requests: client.AppQuantities{CPU: "250m", Memory: "256Mi"}, Limits: client.AppQuantities{CPU: "1", Memory: "512Mi"}}, HealthChecks: client.AppHealthChecks{Readiness: &client.AppProbe{TCP: &client.AppTCPProbe{}}}}),
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
			appImageDigest{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("web").AtName("image"), ConfigValue: tc.value}, &resp)
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
	if patch.Web == nil || patch.Web.Env == nil || patch.Secrets == nil || *patch.Secrets == nil || patch.Domains == nil || len(*patch.Domains) != 1 {
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
	web, _, _, _, diags := appEnvironmentSettings(context.Background(), &prior)
	if diags.HasError() {
		t.Fatal(diags)
	}
	web.Env = []client.AppEnvVar{{Name: "API_KEY", ValueFrom: &client.AppValueFrom{SecretKeyRef: client.AppSecretKeyRef{Secret: "payments", Key: "API_KEY"}}}}
	prior.Web = appWebToObject(web)
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
		name    string
		change  func(*client.AppWeb)
		invalid bool
	}{
		{"defaults", func(*client.AppWeb) {}, false},
		{"two value sources", func(w *client.AppWeb) {
			value := "literal"
			w.Env = []client.AppEnvVar{{Name: "KEY", Value: &value, ValueFrom: &client.AppValueFrom{SecretKeyRef: client.AppSecretKeyRef{Secret: "absent", Key: "KEY"}}}}
		}, true},
		{"long multibyte argument", func(w *client.AppWeb) { w.Args = []string{strings.Repeat("é", 513)} }, true},
		{"both probes", func(w *client.AppWeb) { w.HealthChecks.Readiness.HTTP = &client.AppHTTPProbe{Path: "/"} }, true},
		{"limit below request", func(w *client.AppWeb) { w.Resources.Limits.CPU = "100m" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			web, _, _, _, diags := appEnvironmentSettings(ctx, &base)
			if diags.HasError() {
				t.Fatal(diags)
			}
			tc.change(web)
			webRaw, err := appWebToObject(web).ToTerraformValue(ctx)
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
			raw := testPlanValue(t, sch, map[string]tftypes.Value{"web": webRaw, "secrets": secretsRaw, "domains": domainsRaw})
			var resp resource.ValidateConfigResponse
			r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sch.Schema, Raw: raw}}, &resp)
			if resp.Diagnostics.HasError() != tc.invalid {
				t.Fatalf("configuration validation: %v", resp.Diagnostics)
			}
		})
	}
}

func TestAppReleaseMatchesCanonicalQuantitiesAndReferencedKeys(t *testing.T) {
	m := appTestEnvironmentModel()
	web, _, _, _, diags := appEnvironmentSettings(context.Background(), &m)
	if diags.HasError() {
		t.Fatal(diags)
	}
	web.Resources.Requests.CPU = "0.5"
	env := &client.AppEnvironment{DeploymentRevision: 2, Web: web}
	release := &client.AppRelease{DeploymentRevision: 2, Runtime: client.AppReleaseRuntime{AppWeb: *web}}
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
					web := m.Web.Attributes()
					web["env"] = list
					m.Web = types.ObjectValueMust(appWebTypes, web)
				}
				rawAttrs := map[string]tftypes.Value{}
				for name, value := range map[string]attr.Value{"web": m.Web, "secrets": m.Secrets, "domains": m.Domains} {
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
				// Keep Web and revision equal so secret-difference analysis reaches
				// the env element guard instead of returning on a changed Web.
				prior.Web = m.Web
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
	web, _, _, _, diags := appEnvironmentSettings(ctx, &base)
	if diags.HasError() {
		t.Fatal(diags)
	}
	literal := "info"
	web.Env = []client.AppEnvVar{{Name: "LOG_LEVEL", Value: &literal}}
	for _, tc := range []struct {
		name, app           string
		databases           types.List
		summary, detail, at string
	}{
		{name: "two connections to one database", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", ""), appTestDatabase("reports", "orders-db", "analytics", "REPORTS_URL"))},
		{name: "duplicate name", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", ""), appTestDatabase("main", "users-db", "", "USERS_URL")), summary: "Duplicate connection", detail: "Connection names must be unique.", at: "databases[1].name"},
		{name: "default env used twice", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", ""), appTestDatabase("users", "users-db", "", "DATABASE_URL")), summary: "Duplicate variable", detail: "Each connection needs its own env variable name.", at: "databases[1].env"},
		{name: "env set in web.env", databases: appTestDatabases(appTestDatabase("main", "orders-db", "", "LOG_LEVEL")), summary: "Variable already set", detail: "This env name is already set in web.env. Choose another name for the connection.", at: "databases[0].env"},
		{name: "same database twice", databases: appTestDatabases(appTestDatabase("main", "orders-db", "analytics", ""), appTestDatabase("again", "orders-db", "analytics", "AGAIN_URL")), summary: "Duplicate database", detail: "Each database can be connected once per environment.", at: "databases[1].database"},
		{name: "binding name too long", app: strings.Repeat("a", 32), databases: appTestDatabases(appTestDatabase(strings.Repeat("c", 8), "orders-db", "", "")), summary: "Connection name too long", detail: "The app, environment and connection name together must fit in the 63-character name of the connection's database binding.", at: "databases[0].name"},
		{name: "binding name at the limit", app: strings.Repeat("a", 32), databases: appTestDatabases(appTestDatabase(strings.Repeat("c", 7), "orders-db", "", ""))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app
			if app == "" {
				app = "shop"
			}
			rawAttrs := map[string]tftypes.Value{"app": tftypes.NewValue(tftypes.String, app), "name": tftypes.NewValue(tftypes.String, "production")}
			for name, value := range map[string]attr.Value{"web": appWebToObject(web), "secrets": base.Secrets, "domains": base.Domains, "databases": tc.databases} {
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
			if patch.Databases == nil || len(*patch.Databases) != tc.want || patch.Web == nil || patch.Secrets == nil || patch.Domains == nil {
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
	// An API without connections, and a state written before them, map to
	// the empty lists the schema defaults to, so the next plan is empty.
	mapAppEnvironmentToState(&client.AppEnvironment{}, "", &m)
	if m.Databases.IsNull() || len(m.Databases.Elements()) != 0 || m.DatabaseStatus.IsNull() || len(m.DatabaseStatus.Elements()) != 0 {
		t.Fatalf("absent connections mapped to %v / %v, want empty lists", m.Databases, m.DatabaseStatus)
	}
}

func TestAppReleaseMatchesDatabasesByName(t *testing.T) {
	m := appTestEnvironmentModel()
	web, _, _, _, diags := appEnvironmentSettings(context.Background(), &m)
	if diags.HasError() {
		t.Fatal(diags)
	}
	main, reports := client.AppDatabase{Name: "main", Database: "orders-db", Env: "DATABASE_URL"}, client.AppDatabase{Name: "reports", Database: "orders-db", LogicalDatabase: "analytics", Env: "REPORTS_URL"}
	env := &client.AppEnvironment{DeploymentRevision: 1, Web: web, Databases: []client.AppDatabase{main, reports}}
	release := &client.AppRelease{DeploymentRevision: 1, Runtime: client.AppReleaseRuntime{AppWeb: *web, Databases: []client.AppDatabase{reports, main}}}
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
