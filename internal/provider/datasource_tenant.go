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
	Name                types.String `tfsdk:"name"`
	DisplayName         types.String `tfsdk:"display_name"`
	ContactEmail        types.String `tfsdk:"contact_email"`
	Plan                types.String `tfsdk:"plan"`
	EnforceMetricsLimit types.Bool   `tfsdk:"enforce_metrics_limit"`
	EnforceLogLimit     types.Bool   `tfsdk:"enforce_log_limit"`
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
			"enforce_metrics_limit": schema.BoolAttribute{
				Description: "Whether the tenant blocks metrics ingestion over its plan's allowance.",
				Computed:    true,
			},
			"enforce_log_limit": schema.BoolAttribute{
				Description: "Whether the tenant blocks log ingestion over its plan's allowance.",
				Computed:    true,
			},
		},
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
		Name:                types.StringValue(tenant.Name),
		DisplayName:         types.StringValue(tenant.DisplayName),
		ContactEmail:        types.StringValue(tenant.ContactEmail),
		Plan:                types.StringValue(tenant.Plan),
		EnforceMetricsLimit: types.BoolValue(tenant.EnforceMetricsLimit),
		EnforceLogLimit:     types.BoolValue(tenant.EnforceLogLimit),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
