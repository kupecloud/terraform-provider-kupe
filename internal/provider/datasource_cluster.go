package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ datasource.DataSource = &ClusterDataSource{}

type ClusterDataSource struct {
	client *client.Client
}

type ClusterDataSourceModel struct {
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	Type        types.String `tfsdk:"type"`
	Version     types.String `tfsdk:"version"`
	Phase       types.String `tfsdk:"phase"`
	Endpoint    types.String `tfsdk:"endpoint"`
}

func NewClusterDataSource() datasource.DataSource {
	return &ClusterDataSource{}
}

func (d *ClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (d *ClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing Kupe Cloud cluster by name.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Cluster name to look up.",
				Required:    true,
			},
			"display_name": schema.StringAttribute{
				Description: "Human-readable cluster name.",
				Computed:    true,
			},
			"type": schema.StringAttribute{
				Description: "Cluster type, for example shared or dedicated.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "Current Kubernetes version for the cluster.",
				Computed:    true,
			},
			"phase": schema.StringAttribute{
				Description: "Current cluster phase.",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "Cluster API server endpoint.",
				Computed:    true,
			},
		},
	}
}

func (d *ClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ClusterDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster, _, err := d.client.GetCluster(ctx, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to read cluster", err.Error())
		return
	}

	state.DisplayName = types.StringValue(cluster.DisplayName)
	state.Type = types.StringValue(cluster.Type)
	state.Version = types.StringValue(cluster.Version)
	if cluster.Status != nil {
		state.Phase = types.StringValue(cluster.Status.Phase)
		state.Endpoint = types.StringValue(cluster.Status.Endpoint)
	} else {
		state.Phase = types.StringValue("")
		state.Endpoint = types.StringValue("")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
