package provider

import (
	"context"

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
	Name             types.String `tfsdk:"name"`
	DisplayName      types.String `tfsdk:"display_name"`
	Type             types.String `tfsdk:"type"`
	Version          types.String `tfsdk:"version"`
	HighAvailability types.Bool   `tfsdk:"high_availability"`
	clusterStatusModel
}

func NewClusterDataSource() datasource.DataSource {
	return &ClusterDataSource{}
}

func (d *ClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (d *ClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a Kupe cluster by name.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Cluster name to look up.",
				Required:    true,
			},
			"display_name": schema.StringAttribute{
				Description: "**Deprecated.** Clusters have no separate display name, so this is the same as `name`. " +
					"Use `name` instead.",
				Computed:           true,
				DeprecationMessage: "`display_name` is deprecated and is the same as `name`; use `name` instead.",
			},
			"type": schema.StringAttribute{
				Description: "Cluster type. Only `shared` is supported; the platform refuses `dedicated` with " +
					"`CLUSTER_DEDICATED_UNSUPPORTED`.",
				Computed: true,
			},
			"version": schema.StringAttribute{
				Description: "Kubernetes version the cluster runs.",
				Computed:    true,
			},
			"high_availability": schema.BoolAttribute{
				Description: "Whether the cluster is set up for HA, with a 3-replica control plane. This is the requested setting; `ha_configured` says whether HA is working.",
				Computed:    true,
			},
			"phase": schema.StringAttribute{
				Description: "Current cluster phase, for example `Pending`, `Provisioning`, `Running`, `Upgrading`, `Degraded` or `Terminating`.",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "Cluster API server endpoint.",
				Computed:    true,
			},
			"ha_configured": schema.BoolAttribute{
				Description: "True once all API server replicas and all replicas of the control plane's datastore have been " +
					"`Ready` for the first time.",
				Computed: true,
			},
			"ha_enabled_at": schema.StringAttribute{
				Description: "When `ha_configured` first became true. HA is billed from this moment.",
				Computed:    true,
			},
			"ha_phase": schema.StringAttribute{
				Description: "Summary of HA health: `pending`, `ha-healthy`, `ha-degraded` or `ha-unavailable`. Empty for a cluster without HA.",
				Computed:    true,
			},
			"ha_replicas_ready": schema.Int64Attribute{
				Description: "Number of HA control-plane (API server) replicas that are `Ready`.",
				Computed:    true,
			},
			"ha_replicas_desired": schema.Int64Attribute{
				Description: "Target number of HA API server replicas: 3 when `high_availability = true`, otherwise 0.",
				Computed:    true,
			},
			"ha_etcd_replicas_ready": schema.Int64Attribute{
				Description: "Number of replicas of the control plane's datastore that are `Ready`. The cluster stops " +
					"accepting changes when most of them are down, even while the API server replicas are healthy.",
				Computed: true,
			},
			"ha_etcd_replicas_desired": schema.Int64Attribute{
				Description: "Target number of replicas of the control plane's datastore: 3 when `high_availability = true`, otherwise 0.",
				Computed:    true,
			},
		},
	}
}

func (d *ClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
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
	state.clusterStatusModel = clusterStatusFromAPI(cluster.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
