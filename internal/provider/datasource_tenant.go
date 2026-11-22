package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ datasource.DataSource = &TenantDataSource{}

type TenantDataSource struct {
	client *client.Client
}

type TenantDataSourceModel struct {
	Name             types.String `tfsdk:"name"`
	DisplayName      types.String `tfsdk:"display_name"`
	ContactEmail     types.String `tfsdk:"contact_email"`
	Plan             types.String `tfsdk:"plan"`
	OverageClusters  types.Bool   `tfsdk:"overage_clusters"`
	OverageDatabases types.Bool   `tfsdk:"overage_databases"`
	OverageLogs      types.Bool   `tfsdk:"overage_logs"`
	OverageMetrics   types.Bool   `tfsdk:"overage_metrics"`
}

func NewTenantDataSource() datasource.DataSource {
	return &TenantDataSource{}
}

func (d *TenantDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant"
}

func (d *TenantDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the tenant the provider is configured for.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Tenant name.",
				Computed:    true,
			},
			"display_name": schema.StringAttribute{
				Description: "Tenant display name.",
				Computed:    true,
			},
			"contact_email": schema.StringAttribute{
				Description: "Tenant's main contact email address.",
				Computed:    true,
			},
			"plan": schema.StringAttribute{
				Description: "Name of the tenant's current plan.",
				Computed:    true,
			},
			"overage_clusters":  overageAttribute("clusters"),
			"overage_databases": overageAttribute("databases"),
			"overage_logs":      overageAttribute("logs"),
			"overage_metrics":   overageAttribute("metrics"),
		},
	}
}

// overageAttribute is one of the tenant's overage switches. The spend cap is
// an amount and has no attribute: the provider carries no money.
func overageAttribute(service string) schema.BoolAttribute {
	return schema.BoolAttribute{
		Description: "Whether usage of " + service + " may continue past the plan's credit, up to the tenant's spend cap.",
		Computed:    true,
	}
}

func (d *TenantDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *TenantDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	tenant, _, err := d.client.GetTenant(ctx)
	if err != nil {
		resp.Diagnostics.AddError("failed to read tenant", apiErrorDetail(err))
		return
	}

	state := TenantDataSourceModel{
		Name:             types.StringValue(tenant.Name),
		DisplayName:      types.StringValue(tenant.DisplayName),
		ContactEmail:     types.StringValue(tenant.ContactEmail),
		Plan:             types.StringValue(tenant.Plan),
		OverageClusters:  types.BoolValue(tenant.Overage.Clusters),
		OverageDatabases: types.BoolValue(tenant.Overage.Databases),
		OverageLogs:      types.BoolValue(tenant.Overage.Logs),
		OverageMetrics:   types.BoolValue(tenant.Overage.Metrics),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
