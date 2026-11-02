package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func secretTestKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%02d", i)
	}
	return keys
}

func secretManyTargets(n int) string {
	targets := "["
	for i := range n {
		targets += fmt.Sprintf(`{cluster="prod",namespace="app-%d",keys=%s},`, i, hclStrings(secretTestKeys(64)))
	}
	return targets + "]"
}

func TestAccSecretResourceKeysValidation(t *testing.T) {
	for _, tt := range []struct{ name, sync, want string }{
		{"empty set", secretTargetConfig("[]"), `set must contain at least 1 elements and at most 64 elements`},
		{"null member cannot mean whole entry", secretTargetConfig("[null]"), `Invalid secret key`},
		{"65 keys", secretTargetConfig(hclStrings(secretTestKeys(65))), `set must contain at least 1 elements and at most 64 elements`},
		{"invalid name", secretTargetConfig(`["invalid/key"]`), `Invalid secret key`},
		{"traversal name", secretTargetConfig(`["..hidden"]`), `Invalid secret key`},
		{"aggregate limit", secretManyTargets(17), `Too many selected keys`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{{
				Config: secretConfig(mock.url(), tt.sync, ""), ExpectError: regexp.MustCompile(wrappedText(tt.want)),
			}}})
			if len(mock.secretRequests) != 0 {
				t.Error("invalid configuration reached the API")
			}
		})
	}
	t.Run("1024 selected keys accepted", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{{
			Config: secretConfig(mock.url(), secretManyTargets(16), ""), Check: acc.TestCheckResourceAttr(secretAddr, "sync.#", "16"),
		}}})
	})
}

func TestSecretKeysAggregateUnknownSafe(t *testing.T) {
	ctx := context.Background()
	r := NewSecretResource().(*SecretResource)
	s := testResourceSchema(t, r)
	listType := s.Schema.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["sync"].(tftypes.List)
	objType := listType.ElementType.(tftypes.Object)
	setType := objType.AttributeTypes["keys"]
	target := func(keys any) tftypes.Value {
		return tftypes.NewValue(objType, map[string]tftypes.Value{
			"cluster": tftypes.NewValue(tftypes.String, "prod"), "namespace": tftypes.NewValue(tftypes.String, "app"),
			"secret_name": tftypes.NewValue(tftypes.String, nil), "keys": tftypes.NewValue(setType, keys),
		})
	}
	unknownKeys := make([]tftypes.Value, 64)
	for i := range unknownKeys {
		unknownKeys[i] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	}
	unknownTargets := make([]tftypes.Value, 17)
	for i := range unknownTargets {
		unknownTargets[i] = target(unknownKeys)
	}
	for _, tt := range []struct {
		name  string
		value any
	}{
		{"null list", nil}, {"unknown list", tftypes.UnknownValue},
		{"unknown target", []tftypes.Value{tftypes.NewValue(objType, tftypes.UnknownValue)}},
		{"unknown set", []tftypes.Value{target(tftypes.UnknownValue)}},
		{"unknown names may collapse", unknownTargets},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := testPlanValue(t, s, map[string]tftypes.Value{"sync": tftypes.NewValue(listType, tt.value)})
			var resp resource.ValidateConfigResponse
			r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s.Schema, Raw: raw}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("unknown/null config rejected: %v", resp.Diagnostics)
			}
		})
	}
}

// This fixture serves the pre-#57 schema and writes actual old-shaped state.
// Switching the provider factory in the next CLI step exercises framework
// state upgrade and refresh rather than constructing a new Go model by hand.
type legacySecretProvider struct{ provider.Provider }

func (p *legacySecretProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource {
		return &legacySecretResource{SecretResource: NewSecretResource().(*SecretResource)}
	}}
}

type legacySecretResource struct{ *SecretResource }

func (*legacySecretResource) ConfigValidators(context.Context) []resource.ConfigValidator { return nil }

var legacySyncTypes = map[string]attr.Type{"cluster": types.StringType, "namespace": types.StringType, "secret_name": types.StringType}

type legacySecretModel struct {
	Name       types.String   `tfsdk:"name"`
	SecretPath types.String   `tfsdk:"secret_path"`
	Sync       types.List     `tfsdk:"sync"`
	Phase      types.String   `tfsdk:"phase"`
	ETag       types.String   `tfsdk:"etag"`
	CreatedAt  types.String   `tfsdk:"created_at"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

func (r *legacySecretResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.SecretResource.Schema(ctx, req, resp)
	delete(resp.Schema.Attributes, "sync_status")
	delete(resp.Schema.Attributes, "values_wo")
	delete(resp.Schema.Attributes, "values_wo_version")
	delete(resp.Schema.Attributes, "source_version")
	sync := resp.Schema.Attributes["sync"].(schema.ListNestedAttribute)
	delete(sync.NestedObject.Attributes, "keys")
	resp.Schema.Attributes["sync"] = sync
}

func legacySecretMap(s *client.Secret, etag string, m *legacySecretModel) {
	m.Name = types.StringValue(s.Name)
	m.SecretPath = types.StringValue(s.SecretPath)
	m.ETag = types.StringValue(etag)
	m.CreatedAt = types.StringValue(s.CreatedAt)
	m.Phase = types.StringValue(secretPhase(s))
	if len(s.Sync) == 0 && m.Sync.IsNull() {
		m.Sync = types.ListNull(types.ObjectType{AttrTypes: legacySyncTypes})
		return
	}
	elems := []attr.Value{}
	for _, target := range s.Sync {
		name := types.StringNull()
		if target.SecretName != "" {
			name = types.StringValue(target.SecretName)
		}
		elems = append(elems, types.ObjectValueMust(legacySyncTypes, map[string]attr.Value{"cluster": types.StringValue(target.Cluster), "namespace": types.StringValue(target.Namespace), "secret_name": name}))
	}
	m.Sync = types.ListValueMust(types.ObjectType{AttrTypes: legacySyncTypes}, elems)
}

func (r *legacySecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m legacySecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, etag, err := r.client.CreateSecret(ctx, client.CreateSecretRequest{Name: m.Name.ValueString(), SecretPath: m.SecretPath.ValueString(), Sync: extractSyncTargets(m.Sync)})
	if err != nil {
		resp.Diagnostics.AddError("legacy create", apiErrorDetail(err))
		return
	}
	legacySecretMap(s, etag, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *legacySecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m legacySecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, etag, err := r.client.GetSecret(ctx, m.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("legacy read", apiErrorDetail(err))
		return
	}
	legacySecretMap(s, etag, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *legacySecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m legacySecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSecret(ctx, m.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("legacy delete", apiErrorDetail(err))
	}
}

func TestAccSecretResourceLegacyStateNoDiff(t *testing.T) {
	for _, tt := range []struct {
		name, sync string
		wantNull   bool
	}{
		{"sync omitted", "", true}, {"sync empty", "[]", false}, {"target without keys", secretTargetConfig(""), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			legacy := map[string]func() (tfprotov6.ProviderServer, error){"kupe": providerserver.NewProtocol6WithError(&legacySecretProvider{Provider: New("legacy")()})}
			config := secretConfig(mock.url(), tt.sync, "")
			statusCount := "0"
			if tt.sync != "" && tt.sync != "[]" {
				statusCount = "1"
			}
			syncCheck := acc.TestCheckResourceAttr(secretAddr, "sync.#", statusCount)
			if tt.wantNull {
				syncCheck = acc.TestCheckNoResourceAttr(secretAddr, "sync.#")
			}
			acc.UnitTest(t, acc.TestCase{Steps: []acc.TestStep{
				{Config: config, ProtoV6ProviderFactories: legacy, Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckNoResourceAttr(secretAddr, "sync_status.#"), acc.TestCheckNoResourceAttr(secretAddr, "sync.0.keys.#"), syncCheck,
				)},
				{Config: config, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(secretAddr, "sync_status.#", statusCount), acc.TestCheckNoResourceAttr(secretAddr, "sync.0.keys.#"), syncCheck,
				)},
			}})
			if len(mock.secretRequests) != 1 {
				t.Errorf("schema addition caused %d writes, want initial POST only", len(mock.secretRequests))
			}
		})
	}
}
