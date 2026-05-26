package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Cluster phase the provider considers "ready" — Create and Update wait
// for this value before returning. All other observed phases (Pending,
// Provisioning, Upgrading, Degraded) are treated as in-progress and
// keep the poll going until either Running is reached or the
// user-configured Create/Update timeout fires.
const clusterPhaseRunning = "Running"

// Default timeouts for cluster operations. Users can override via the
// resource's `timeouts` block in HCL; these are the framework defaults
// applied when the block is omitted. Provisioning typically takes 3-8
// minutes on dev; 15 min covers worst-case vCluster start-up plus
// add-on reconcile headroom.
const (
	defaultClusterCreateTimeout = 15 * time.Minute
	defaultClusterUpdateTimeout = 15 * time.Minute
	defaultClusterDeleteTimeout = 10 * time.Minute
)

var (
	_ resource.Resource                = &ClusterResource{}
	_ resource.ResourceWithImportState = &ClusterResource{}
)

type ClusterResource struct {
	client *client.Client
}

type ClusterResourceModel struct {
	Name              types.String           `tfsdk:"name"`
	DisplayName       types.String           `tfsdk:"display_name"`
	Type              types.String           `tfsdk:"type"`
	Version           types.String           `tfsdk:"version"`
	Resources         *ClusterResourcesModel `tfsdk:"resources"`
	HighAvailability  types.Bool             `tfsdk:"high_availability"`
	Phase             types.String           `tfsdk:"phase"`
	Endpoint          types.String           `tfsdk:"endpoint"`
	HAConfigured      types.Bool             `tfsdk:"ha_configured"`
	HAEnabledAt       types.String           `tfsdk:"ha_enabled_at"`
	HAPhase           types.String           `tfsdk:"ha_phase"`
	HAReplicasReady   types.Int64            `tfsdk:"ha_replicas_ready"`
	HAReplicasDesired types.Int64            `tfsdk:"ha_replicas_desired"`
	ETag              types.String           `tfsdk:"etag"`
	CreatedAt         types.String           `tfsdk:"created_at"`
	Timeouts          timeouts.Value         `tfsdk:"timeouts"`
}

type ClusterResourcesModel struct {
	CPU     types.String `tfsdk:"cpu"`
	Memory  types.String `tfsdk:"memory"`
	Storage types.String `tfsdk:"storage"`
}

func NewClusterResource() resource.Resource {
	return &ClusterResource{}
}

func (r *ClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (r *ClusterResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe cluster for a tenant. Create and Update wait for the " +
			"cluster to reach phase=Running before returning; Delete waits for the underlying " +
			"ManagedCluster CR to finish terminating. Timeouts are configurable via the " +
			"`timeouts` block (defaults: create/update 15m, delete 10m).\n\n" +
			"### Destroy semantics\n\n" +
			"`terraform destroy` (or removing this resource from configuration) is **non-recoverable**. " +
			"When the cluster is deleted, the Kupe platform will:\n\n" +
			"- Permanently remove every workload running inside the cluster, along with its storage.\n" +
			"- Remove the cluster's public DNS endpoint.\n\n" +
			"The same contract applies whether you delete via Terraform, the `kupe` CLI, or the " +
			"Kupe Console.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Cluster name (immutable after creation).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"display_name": schema.StringAttribute{
				Description: "Human-readable display name (immutable after creation).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Description: "Cluster type. Valid values are shared and dedicated. Immutable after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("shared", "dedicated"),
				},
			},
			"version": schema.StringAttribute{
				Description: "Kubernetes version (e.g., 1.31).",
				Optional:    true,
				Computed:    true,
			},
			"resources": schema.SingleNestedAttribute{
				Description: "Resource limits for the cluster. Updates are sent as a JSON Merge " +
					"Patch (RFC 7396) — fields you remove from this block are **left unchanged** " +
					"on the server, not cleared. To clear all resource limits, remove the entire " +
					"`resources` block. To change an individual field, write it explicitly.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Description: "CPU limit (e.g., 4, 500m).",
						Optional:    true,
						Computed:    true,
					},
					"memory": schema.StringAttribute{
						Description: "Memory limit (e.g., 16Gi, 512Mi).",
						Optional:    true,
						Computed:    true,
					},
					"storage": schema.StringAttribute{
						Description: "Storage limit (e.g., 100Gi).",
						Optional:    true,
						Computed:    true,
					},
				},
			},
			"high_availability": schema.BoolAttribute{
				Description: "Enable a 3-replica HA control plane with HA etcd (chart-managed external etcd StatefulSet), " +
					"hard anti-affinity, and encrypted-at-rest etcd via a per-cluster AES-CBC key. " +
					"Adds an hourly charge — see `data.kupe_plan` for the rate. Default `false`.\n\n" +
					"**Create-time-only.** This attribute is effectively immutable: changing it on an existing resource forces " +
					"Terraform to **replace** the cluster (destroy + create). The operator rejects both directions of the " +
					"toggle with canonical error codes (`HA_ENABLE_ON_EXISTING_UNSUPPORTED`, `HA_DISABLE_UNSUPPORTED`) — " +
					"`RequiresReplace` here makes Terraform's plan reflect that reality up front. Use a blue-green swap " +
					"workflow if you need HA on an existing cluster: create a new HA cluster, redeploy via GitOps, swap traffic, " +
					"then destroy the old.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
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
				Description: "True once the operator has confirmed 3/3 HA control-plane replicas ready. " +
					"Distinct from `high_availability` (the requested state) — read this attribute when downstream " +
					"automation needs to wait for HA to be operationally available, not just toggled on.",
				Computed: true,
			},
			"ha_enabled_at": schema.StringAttribute{
				Description: "Timestamp when `ha_configured` first became true. This is the billing anchor — HA hours " +
					"accrue from this moment. Stamped once, never updated, never cleared.",
				Computed: true,
			},
			"ha_phase": schema.StringAttribute{
				Description: "Consumer-friendly HA rollup. One of `pending`, `ha-healthy`, `ha-degraded`, `ha-unavailable`. " +
					"Empty for non-HA clusters. Use this in downstream automation to branch on operational state without " +
					"inspecting individual conditions.",
				Computed: true,
			},
			"ha_replicas_ready": schema.Int64Attribute{
				Description: "Count of HA control-plane replicas currently `Ready`. Zero for non-HA clusters.",
				Computed:    true,
			},
			"ha_replicas_desired": schema.Int64Attribute{
				Description: "Target HA replica count (3 when `high_availability = true`, 0 otherwise).",
				Computed:    true,
			},
			"etag": schema.StringAttribute{
				// etag intentionally has no UseStateForUnknown: it changes on
				// every PATCH/PUT and we want the plan to honestly say
				// "known after apply" on any update.
				Description: "Resource version used for optimistic locking during updates.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the cluster was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

func (r *ClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *ClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := client.CreateClusterRequest{
		Name:             plan.Name.ValueString(),
		DisplayName:      plan.DisplayName.ValueString(),
		Type:             plan.Type.ValueString(),
		Version:          plan.Version.ValueString(),
		HighAvailability: plan.HighAvailability.ValueBool(),
	}

	if plan.Resources != nil {
		createReq.Resources = clusterResourcesFromPlan(plan.Resources)
	}

	cluster, etag, err := r.client.CreateCluster(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", apiErrorDetail(err))
		return
	}

	// Persist the freshly-accepted state immediately so a Ctrl-C
	// during the wait below leaves Terraform with the resource on
	// record (with phase=Pending or whatever the API just returned)
	// rather than orphaning it.
	mapClusterToState(cluster, etag, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Block until the operator finishes provisioning. The API
	// returns 201 the moment the K8s CR is accepted (phase=Pending);
	// real readiness — kubeconfig endpoint populated, vCluster
	// reachable — happens asynchronously over a few minutes. Without
	// this wait, downstream resources that reference
	// kupe_cluster.foo.endpoint get an empty string at apply time.
	// User-overridable via the `timeouts.create` block (default 15m).
	timeout, diags := plan.Timeouts.Create(ctx, defaultClusterCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitForClusterReady(ctx, timeout, plan.Name.ValueString(), &plan, &resp.State, &resp.Diagnostics, "create")
}

func (r *ClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster, etag, err := r.client.GetCluster(ctx, state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read cluster", apiErrorDetail(err))
		return
	}

	mapClusterToState(cluster, etag, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ClusterResourceModel
	var state ClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	patchReq := client.PatchClusterRequest{}
	hasChanges := false

	if !plan.Version.Equal(state.Version) {
		v := plan.Version.ValueString()
		patchReq.Version = &v
		hasChanges = true
	}

	switch {
	case plan.Resources != nil && (state.Resources == nil ||
		!plan.Resources.CPU.Equal(state.Resources.CPU) ||
		!plan.Resources.Memory.Equal(state.Resources.Memory) ||
		!plan.Resources.Storage.Equal(state.Resources.Storage)):
		patchReq.Resources = clusterResourcesFromPlan(plan.Resources)
		hasChanges = true
	case plan.Resources == nil && state.Resources != nil:
		// User removed the resources block — send empty to clear.
		patchReq.Resources = &client.ClusterResource{}
		hasChanges = true
	}

	// HA changes are handled by RequiresReplace on the high_availability
	// attribute — Terraform will destroy + recreate the cluster rather
	// than Update, so this branch never sees an HA diff.

	if hasChanges {
		cluster, etag, err := r.client.UpdateCluster(ctx, plan.Name.ValueString(), state.ETag.ValueString(), patchReq)
		if err != nil {
			resp.Diagnostics.AddError("failed to update cluster", apiErrorDetail(err))
			return
		}
		mapClusterToState(cluster, etag, &plan)
	} else {
		// No API changes but always refresh computed fields
		cluster, etag, err := r.client.GetCluster(ctx, plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("failed to read cluster", apiErrorDetail(err))
			return
		}
		mapClusterToState(cluster, etag, &plan)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !hasChanges {
		return
	}

	// Version bumps and resource changes trigger a rolling upgrade
	// via the operator (Running → Upgrading → Running). Wait for
	// the cluster to settle back at Running so downstream resources
	// see the upgraded version, not the still-rolling intermediate
	// state. User-overridable via `timeouts.update` (default 15m).
	timeout, diags := plan.Timeouts.Update(ctx, defaultClusterUpdateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitForClusterReady(ctx, timeout, plan.Name.ValueString(), &plan, &resp.State, &resp.Diagnostics, "update")
}

func (r *ClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := state.Name.ValueString()
	if err := r.client.DeleteCluster(ctx, name); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to delete cluster", apiErrorDetail(err))
		return
	}

	// Poll until the ManagedCluster CR is genuinely gone. kupe-api's
	// DELETE returns 204 once K8s accepts the request, but the
	// operator's finalizer drives a multi-minute teardown (vCluster
	// stop, namespace cleanup). Without this wait, an immediate
	// re-apply with the same cluster name 409s on the still-
	// terminating CR. User-overridable via `timeouts.delete` (default
	// 10m). On timeout we surface a warning so users can re-run
	// destroy or manually intervene without the apply itself failing.
	timeout, diags := state.Timeouts.Delete(ctx, defaultClusterDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := waitForCondition(waitCtx, func(c context.Context) (bool, error) {
		_, _, err := r.client.GetCluster(c, name)
		return client.IsNotFound(err), nil
	})
	if err != nil {
		resp.Diagnostics.AddWarning(
			"cluster still terminating",
			fmt.Sprintf("DELETE was accepted but cluster %q is still terminating after the configured "+
				"delete timeout. Kupe Cloud is still cleaning up the underlying resources. Re-running "+
				"`terraform destroy` will wait again, and the same cluster name cannot be reused until "+
				"termination completes. If the cluster stays in this state, check it in the Kupe "+
				"Console or contact Kupe Cloud support. Override with "+
				"`timeouts.delete = \"30m\"` for clusters with heavier teardown.", name),
		)
	}
}

// waitForClusterReady polls GetCluster until the observed phase is
// "Running" or the context expires. Refreshes the resource's framework
// state on every poll so an interrupted apply leaves Terraform with the
// latest observed values rather than the initial Create/Update response.
// kind is "create" or "update", surfaced in the warning so users can
// distinguish the two paths.
func (r *ClusterResource) waitForClusterReady(
	ctx context.Context,
	timeout time.Duration,
	name string,
	plan *ClusterResourceModel,
	state *tfsdk.State,
	diags *diag.Diagnostics,
	kind string,
) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := waitForCondition(waitCtx, func(c context.Context) (bool, error) {
		cluster, etag, err := r.client.GetCluster(c, name)
		if err != nil {
			// Transient — keep polling. The eventual deadline will
			// surface a timeout if the API stays unreachable. We
			// deliberately discard err here per waitForCondition's
			// contract; a persistent fault becomes a timeout warning.
			return false, nil //nolint:nilerr // transient errors keep the poll going
		}
		mapClusterToState(cluster, etag, plan)
		// Persist what we just observed so a Ctrl-C between polls
		// doesn't lose the most recent phase/endpoint.
		diags.Append(state.Set(c, plan)...)
		phase := ""
		if cluster.Status != nil {
			phase = cluster.Status.Phase
		}
		return phase == clusterPhaseRunning, nil
	})
	if err != nil {
		diags.AddWarning(
			fmt.Sprintf("cluster %s timed out before reaching Running", kind),
			fmt.Sprintf("cluster %q reached phase=%q before the %s timeout fired. Wait a few minutes "+
				"and re-run `terraform apply` to pick up the Running state — provisioning continues "+
				"in the background and a subsequent apply will be a no-op once the cluster is ready. "+
				"If the cluster stays in this phase for longer than expected, check the cluster status "+
				"in the Kupe Console or contact Kupe Cloud support. Override with "+
				"`timeouts.%s = \"30m\"` for slow environments.", name, plan.Phase.ValueString(), kind, kind),
		)
	}
}

func (r *ClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

func mapClusterToState(c *client.Cluster, etag string, state *ClusterResourceModel) {
	state.Name = types.StringValue(c.Name)
	state.DisplayName = types.StringValue(c.DisplayName)
	state.Type = types.StringValue(c.Type)
	state.Version = types.StringValue(c.Version)
	state.HighAvailability = types.BoolValue(c.HighAvailability)
	state.ETag = types.StringValue(etag)
	state.CreatedAt = types.StringValue(c.CreatedAt)

	if c.Status != nil {
		state.Phase = types.StringValue(c.Status.Phase)
		state.Endpoint = types.StringValue(c.Status.Endpoint)
		state.HAConfigured = types.BoolValue(c.Status.HAConfigured)
		state.HAEnabledAt = types.StringValue(c.Status.HAEnabledAt)
		state.HAPhase = types.StringValue(c.Status.HAPhase)
		state.HAReplicasReady = types.Int64Value(int64(c.Status.HAReplicasReady))
		state.HAReplicasDesired = types.Int64Value(int64(c.Status.HAReplicasDesired))
	} else {
		state.Phase = types.StringValue("")
		state.Endpoint = types.StringValue("")
		state.HAConfigured = types.BoolValue(false)
		state.HAEnabledAt = types.StringValue("")
		state.HAPhase = types.StringValue("")
		state.HAReplicasReady = types.Int64Value(0)
		state.HAReplicasDesired = types.Int64Value(0)
	}

	if c.Resources != nil && (c.Resources.CPU != "" || c.Resources.Memory != "" || c.Resources.Storage != "") {
		state.Resources = &ClusterResourcesModel{
			CPU:     stringOrNull(c.Resources.CPU),
			Memory:  stringOrNull(c.Resources.Memory),
			Storage: stringOrNull(c.Resources.Storage),
		}
	} else {
		state.Resources = nil
	}
}

// clusterResourcesFromPlan builds a ClusterResource only including
// non-null fields so omitted nested values are absent from the JSON body
// rather than sent as empty strings.
func clusterResourcesFromPlan(m *ClusterResourcesModel) *client.ClusterResource {
	r := &client.ClusterResource{}
	if !m.CPU.IsNull() && !m.CPU.IsUnknown() {
		r.CPU = m.CPU.ValueString()
	}
	if !m.Memory.IsNull() && !m.Memory.IsUnknown() {
		r.Memory = m.Memory.ValueString()
	}
	if !m.Storage.IsNull() && !m.Storage.IsUnknown() {
		r.Storage = m.Storage.ValueString()
	}
	return r
}

// stringOrNull returns types.StringNull for empty API values so terraform
// state doesn't contain "" for fields the user never set — that prevents
// perpetual diffs on partial resources blocks (e.g., memory-only).
func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
