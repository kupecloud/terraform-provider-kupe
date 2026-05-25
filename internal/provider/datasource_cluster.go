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
	Name              types.String `tfsdk:"name"`
	DisplayName       types.String `tfsdk:"display_name"`
	Type              types.String `tfsdk:"type"`
	Version           types.String `tfsdk:"version"`
	HighAvailability  types.Bool   `tfsdk:"high_availability"`
	Phase             types.String `tfsdk:"phase"`
	Endpoint          types.String `tfsdk:"endpoint"`
	HAConfigured      types.Bool   `tfsdk:"ha_configured"`
	HAEnabledAt       types.String `tfsdk:"ha_enabled_at"`
	HAPhase           types.String `tfsdk:"ha_phase"`
	HAReplicasReady   types.Int64  `tfsdk:"ha_replicas_ready"`
	HAReplicasDesired types.Int64  `tfsdk:"ha_replicas_desired"`
}

func NewClusterDataSource() datasource.DataSource {
	return &ClusterDataSource{}
}

func (d *ClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (d *ClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing Kupe cluster by name.",
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
			"high_availability": schema.BoolAttribute{
				Description: "Whether the cluster is configured with HA (3-replica control plane). Reflects spec, not operational state — see `ha_configured`.",
				Computed:    true,
			},
			"phase": schema.StringAttribute{
				Description: "Current cluster phase, for example Pending, Provisioning, Running, Migrating, or Degraded.",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "Cluster API server endpoint.",
				Computed:    true,
			},
			"ha_configured": schema.BoolAttribute{
				Description: "True once the operator has confirmed 3/3 HA control-plane replicas ready.",
				Computed:    true,
			},
			"ha_enabled_at": schema.StringAttribute{
				Description: "Timestamp when `ha_configured` first became true (billing anchor).",
				Computed:    true,
			},
			"ha_phase": schema.StringAttribute{
				Description: "Consumer-friendly HA rollup. One of `pending`, `migrating`, `ha-healthy`, `ha-degraded`, `ha-unavailable`. Empty for non-HA clusters.",
				Computed:    true,
			},
			"ha_replicas_ready": schema.Int64Attribute{
				Description: "Count of HA control-plane replicas currently `Ready`.",
				Computed:    true,
			},
			"ha_replicas_desired": schema.Int64Attribute{
				Description: "Target HA replica count (3 when `high_availability = true`, 0 otherwise).",
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
		resp.Diagnostics.AddError("failed to read cluster", apiErrorDetail(err))
		return
	}

	state.DisplayName = types.StringValue(cluster.DisplayName)
	state.Type = types.StringValue(cluster.Type)
	state.Version = types.StringValue(cluster.Version)
	state.HighAvailability = types.BoolValue(cluster.HighAvailability)
	if cluster.Status != nil {
		state.Phase = types.StringValue(cluster.Status.Phase)
		state.Endpoint = types.StringValue(cluster.Status.Endpoint)
		state.HAConfigured = types.BoolValue(cluster.Status.HAConfigured)
		state.HAEnabledAt = types.StringValue(cluster.Status.HAEnabledAt)
		state.HAPhase = types.StringValue(cluster.Status.HAPhase)
		state.HAReplicasReady = types.Int64Value(int64(cluster.Status.HAReplicasReady))
		state.HAReplicasDesired = types.Int64Value(int64(cluster.Status.HAReplicasDesired))
	} else {
		state.Phase = types.StringValue("")
		state.Endpoint = types.StringValue("")
		state.HAConfigured = types.BoolValue(false)
		state.HAEnabledAt = types.StringValue("")
		state.HAPhase = types.StringValue("")
		state.HAReplicasReady = types.Int64Value(0)
		state.HAReplicasDesired = types.Int64Value(0)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
