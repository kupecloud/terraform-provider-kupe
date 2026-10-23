package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ datasource.DataSource = &PlanDataSource{}

type PlanDataSource struct {
	client *client.Client
}

type PlanDataSourceModel struct {
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	// PlatformFee is deprecated and always null: Terraform returns no
	// platform costs (kupecloud/kupe-api#42). It stays in the schema so
	// configurations that reference it keep working until a major version
	// removes it.
	PlatformFee  types.String `tfsdk:"platform_fee"`
	MaxClusters  types.Int64  `tfsdk:"max_clusters"`
	MaxDatabases types.Int64  `tfsdk:"max_databases"`
	// RegistryMaxStorage and RegistryIncludedStorage are the plan's
	// registry allowance, null when the plan includes no registry.
	RegistryMaxStorage      types.String `tfsdk:"registry_max_storage"`
	RegistryIncludedStorage types.String `tfsdk:"registry_included_storage"`
}

func NewPlanDataSource() datasource.DataSource {
	return &PlanDataSource{}
}

func (d *PlanDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plan"
}

func (d *PlanDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a Kupe Cloud plan by name.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Plan name to look up.",
				Required:    true,
			},
			"display_name": schema.StringAttribute{
				Description: "Human-readable plan name.",
				Computed:    true,
			},
			"platform_fee": schema.StringAttribute{
				Description: "Deprecated: not returned (no platform costs in Terraform).",
				DeprecationMessage: "platform_fee is always null: Terraform returns no platform costs. " +
					"Remove references to it; a future major version removes the attribute.",
				Computed: true,
			},
			"max_clusters": schema.Int64Attribute{
				Description: "Maximum number of clusters allowed on the plan. `0` means unlimited.",
				Computed:    true,
			},
			"max_databases": schema.Int64Attribute{
				Description: "Maximum number of databases allowed on the plan. `0` means unlimited. Null when the API does not report a database limit for the plan.",
				Computed:    true,
			},
			"registry_max_storage": schema.StringAttribute{
				Description: "Most a registry may store on the plan, with a binary unit, e.g. `5Gi`. `0` means " +
					"unlimited. Null when the plan includes no registry.",
				Computed: true,
			},
			"registry_included_storage": schema.StringAttribute{
				Description: "Registry storage the plan includes, with a binary unit, e.g. `1Gi`. Null when the plan " +
					"includes no registry.",
				Computed: true,
			},
		},
	}
}

func (d *PlanDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *PlanDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state PlanDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan, err := d.client.GetPlan(ctx, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to read plan", apiErrorDetail(err))
		return
	}

	state.DisplayName = types.StringValue(plan.DisplayName)
	state.PlatformFee = types.StringNull()
	state.MaxClusters = types.Int64Value(plan.MaxClusters)
	state.MaxDatabases = types.Int64PointerValue(plan.MaxDatabases)
	state.RegistryMaxStorage, state.RegistryIncludedStorage = types.StringNull(), types.StringNull()
	if plan.Registry != nil {
		state.RegistryMaxStorage = types.StringValue(plan.Registry.MaxStorage)
		state.RegistryIncludedStorage = types.StringValue(plan.Registry.IncludedStorage)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
