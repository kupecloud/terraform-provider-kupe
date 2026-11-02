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
		Web:     appWebToObject(&client.AppWeb{Image: "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("a", 64), Port: 8080, Replicas: 1, Resources: client.AppResources{Requests: client.AppQuantities{CPU: "250m", Memory: "256Mi"}, Limits: client.AppQuantities{CPU: "1", Memory: "512Mi"}}, HealthChecks: client.AppHealthChecks{Readiness: &client.AppProbe{TCP: &client.AppTCPProbe{}}}}),
		Secrets: types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, []attr.Value{}),
		Domains: types.SetValueMust(types.StringType, []attr.Value{}),
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
	web, _, _, diags := appEnvironmentSettings(context.Background(), &prior)
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
			web, _, _, diags := appEnvironmentSettings(ctx, &base)
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
	web, _, _, diags := appEnvironmentSettings(context.Background(), &m)
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
					if _, _, _, d := appEnvironmentSettings(ctx, &m); !d.HasError() {
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
