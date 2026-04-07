package provider

import (
	"context"
	"fmt"

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
		Description: "Reads the current tenant's details.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Tenant name.",
				Computed:    true,
			},
			"display_name": schema.StringAttribute{
				Description: "Human-readable tenant name.",
				Computed:    true,
			},
			"contact_email": schema.StringAttribute{
				Description: "Primary tenant contact email.",
				Computed:    true,
			},
			"plan": schema.StringAttribute{
				Description: "Current subscribed plan name.",
				Computed:    true,
			},
			"enforce_metrics_limit": schema.BoolAttribute{
				Description: "Whether metrics ingestion overage is blocked at the tenant level.",
				Computed:    true,
			},
			"enforce_log_limit": schema.BoolAttribute{
				Description: "Whether log ingestion overage is blocked at the tenant level.",
				Computed:    true,
			},
		},
	}
}

func (d *TenantDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	d.client = c
}

func (d *TenantDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	tenant, _, err := d.client.GetTenant(ctx)
	if err != nil {
		resp.Diagnostics.AddError("failed to read tenant", err.Error())
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
