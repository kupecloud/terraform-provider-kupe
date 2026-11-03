package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Serve the actual pre-access shape, so the next step upgrades and refreshes
// persisted old-shaped state through the framework and a real CLI.
type legacyDatabaseBindingProvider struct{ provider.Provider }

func (*legacyDatabaseBindingProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewDatabaseResource, func() resource.Resource {
		return &legacyDatabaseBindingResource{DatabaseBindingResource: NewDatabaseBindingResource().(*DatabaseBindingResource)}
	}}
}

type legacyDatabaseBindingResource struct{ *DatabaseBindingResource }

func (*legacyDatabaseBindingResource) ValidateConfig(context.Context, resource.ValidateConfigRequest, *resource.ValidateConfigResponse) {
}
func (r *legacyDatabaseBindingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.DatabaseBindingResource.Schema(ctx, req, resp)
	for _, key := range []string{"role_name", "credentials", "observed_rotation"} {
		delete(resp.Schema.Attributes, key)
	}
	status := resp.Schema.Attributes["namespace_status"].(schema.ListNestedAttribute)
	delete(status.NestedObject.Attributes, "delivered_rotation")
	resp.Schema.Attributes["namespace_status"] = status
}

type legacyDatabaseBindingModel struct {
	Name            types.String   `tfsdk:"name"`
	Database        types.String   `tfsdk:"database"`
	Cluster         types.String   `tfsdk:"cluster"`
	Namespaces      types.Set      `tfsdk:"namespaces"`
	Role            types.String   `tfsdk:"role"`
	SecretName      types.String   `tfsdk:"secret_name"`
	Phase           types.String   `tfsdk:"phase"`
	NamespaceStatus types.List     `tfsdk:"namespace_status"`
	ETag            types.String   `tfsdk:"etag"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

var legacyBindingNamespaceType = types.ObjectType{AttrTypes: map[string]attr.Type{"name": types.StringType, "host": types.StringType, "phase": types.StringType, "message": types.StringType}}

func mapLegacyDatabaseBinding(b *client.DatabaseBinding, etag string, m *legacyDatabaseBindingModel) {
	m.Name = types.StringValue(b.Name)
	m.Database = types.StringValue(b.Database)
	m.Cluster = types.StringValue(b.Cluster)
	m.Namespaces = stringSet(b.Namespaces)
	m.Role = types.StringValue(b.Role)
	m.ETag = types.StringValue(etag)
	m.SecretName = types.StringValue(b.Status.SecretName)
	m.Phase = types.StringValue(b.Status.Phase)
	elements := make([]attr.Value, 0, len(b.Status.Namespaces))
	for _, ns := range b.Status.Namespaces {
		elements = append(elements, types.ObjectValueMust(legacyBindingNamespaceType.AttrTypes, map[string]attr.Value{"name": types.StringValue(ns.Name), "host": types.StringValue(ns.Host), "phase": types.StringValue(ns.Phase), "message": types.StringValue(ns.Message)}))
	}
	m.NamespaceStatus = types.ListValueMust(legacyBindingNamespaceType, elements)
}
func (r *legacyDatabaseBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m legacyDatabaseBindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, _, err := r.client.CreateDatabaseBinding(ctx, m.Database.ValueString(), client.CreateDatabaseBindingRequest{Name: m.Name.ValueString(), Cluster: m.Cluster.ValueString(), Namespaces: setStrings(m.Namespaces), Role: m.Role.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("legacy create", apiErrorDetail(err))
		return
	}
	b, etag, err := r.client.GetDatabaseBinding(ctx, m.Database.ValueString(), m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("legacy read", apiErrorDetail(err))
		return
	}
	mapLegacyDatabaseBinding(b, etag, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *legacyDatabaseBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m legacyDatabaseBindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, etag, err := r.client.GetDatabaseBinding(ctx, m.Database.ValueString(), m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("legacy read", apiErrorDetail(err))
		return
	}
	mapLegacyDatabaseBinding(b, etag, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *legacyDatabaseBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m legacyDatabaseBindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDatabaseBinding(ctx, m.Database.ValueString(), m.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("legacy delete", apiErrorDetail(err))
	}
}
func TestAccDatabaseBindingLegacyStateNoDiff(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	old := map[string]func() (tfprotov6.ProviderServer, error){"kupe": providerserver.NewProtocol6WithError(&legacyDatabaseBindingProvider{Provider: New("legacy")()})}
	config := testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "worker"), "readwrite", "")
	acc.UnitTest(t, acc.TestCase{Steps: []acc.TestStep{
		{Config: config, ProtoV6ProviderFactories: old, Check: acc.TestCheckNoResourceAttr("kupe_database_binding.test", "credentials.dedicated")},
		{Config: config, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: acc.ComposeAggregateTestCheckFunc(acc.TestCheckResourceAttr("kupe_database_binding.test", "credentials.dedicated", "false"), acc.TestCheckResourceAttr("kupe_database_binding.test", "namespace_status.0.delivered_rotation", ""), acc.TestCheckResourceAttr("kupe_database_binding.test", "namespace_status.#", "2"))},
	}})
	if len(mock.databaseBindingRequests) != 1 {
		t.Errorf("old state refresh caused API writes: %v", mock.databaseBindingRequests)
	} // initial create only
}
