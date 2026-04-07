package provider

import (
	"context"
	"fmt"

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
	PlatformFee types.String `tfsdk:"platform_fee"`
	MaxClusters types.Int64  `tfsdk:"max_clusters"`
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
				Description: "Base monthly platform fee for the plan.",
				Computed:    true,
			},
			"max_clusters": schema.Int64Attribute{
				Description: "Maximum number of clusters allowed on the plan.",
				Computed:    true,
			},
		},
	}
}

func (d *PlanDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *PlanDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state PlanDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan, err := d.client.GetPlan(ctx, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to read plan", err.Error())
		return
	}

	state.DisplayName = types.StringValue(plan.DisplayName)
	state.PlatformFee = types.StringValue(plan.PlatformFee)
	state.MaxClusters = types.Int64Value(plan.MaxClusters)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
