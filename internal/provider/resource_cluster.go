package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Cluster phases Create and Update act on. Running is the one they wait
// for; Pending, Provisioning and Upgrading are in progress; Terminating ends
// the wait at once and Degraded, once the platform has seen the change
// (observedGeneration caught up with generation), after it has lasted
// clusterDegradedGrace.
const (
	clusterPhaseRunning  = "Running"
	clusterPhaseDegraded = "Degraded"
)

// clusterDegradedGrace is how long Create and Update ride out a Degraded
// cluster that has seen the change before failing. The platform stamps
// observedGeneration in the pass that hands the change to the cluster's
// deployment, but judges the phase from the deployment's status read in that
// same pass, from before it retried: a cluster Degraded by a failed
// deployment sync reads Degraded at the new generation until the next pass,
// even when the change heals it. 2 minutes is the platform's own grace for a
// running cluster's blips. A var so tests can shorten it.
var clusterDegradedGrace = 2 * time.Minute

// Default timeouts for cluster operations. Users can override via the
// resource's `timeouts` attribute in HCL; these are the framework defaults
// applied when the attribute is omitted. Provisioning typically takes 3-8
// minutes on dev; 15 min covers worst-case vCluster start-up plus
// add-on reconcile headroom.
const (
	defaultClusterCreateTimeout = 15 * time.Minute
	defaultClusterUpdateTimeout = 15 * time.Minute
	defaultClusterDeleteTimeout = 10 * time.Minute
)

var (
	_ resource.Resource                = &ClusterResource{}
	_ resource.ResourceWithConfigure   = &ClusterResource{}
	_ resource.ResourceWithImportState = &ClusterResource{}
)

// ClusterResource is kupe_cluster. Read, Delete, Configure and ImportState
// come from managedResource.
type ClusterResource struct {
	managedResource[ClusterResourceModel, client.Cluster]
}

// clusterStatusModel holds the status attributes the kupe_cluster resource
// and data source share.
type clusterStatusModel struct {
	Phase                 types.String `tfsdk:"phase"`
	Endpoint              types.String `tfsdk:"endpoint"`
	HAConfigured          types.Bool   `tfsdk:"ha_configured"`
	HAEnabledAt           types.String `tfsdk:"ha_enabled_at"`
	HAPhase               types.String `tfsdk:"ha_phase"`
	HAReplicasReady       types.Int64  `tfsdk:"ha_replicas_ready"`
	HAReplicasDesired     types.Int64  `tfsdk:"ha_replicas_desired"`
	HAEtcdReplicasReady   types.Int64  `tfsdk:"ha_etcd_replicas_ready"`
	HAEtcdReplicasDesired types.Int64  `tfsdk:"ha_etcd_replicas_desired"`
}

type ClusterResourceModel struct {
	Name             types.String    `tfsdk:"name"`
	DisplayName      types.String    `tfsdk:"display_name"`
	Type             types.String    `tfsdk:"type"`
	Version          types.String    `tfsdk:"version"`
	Resources        *ResourcesModel `tfsdk:"resources"`
	HighAvailability types.Bool      `tfsdk:"high_availability"`
	clusterStatusModel
	ETag      types.String   `tfsdk:"etag"`
	CreatedAt types.String   `tfsdk:"created_at"`
	Timeouts  timeouts.Value `tfsdk:"timeouts"`
}

func NewClusterResource() resource.Resource {
	return &ClusterResource{managedResource[ClusterResourceModel, client.Cluster]{kind: &clusterKind}}
}

// clusterKind is the cluster's lifecycle for managedResource.
var clusterKind = serviceKind[ClusterResourceModel, client.Cluster]{
	noun: "cluster",
	name: func(m *ClusterResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *ClusterResourceModel) (*client.Cluster, string, error) {
		return c.GetCluster(ctx, m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *ClusterResourceModel) error {
		return c.DeleteCluster(ctx, m.Name.ValueString())
	},
	mapToState: mapClusterToState,
	timeoutsOf: func(m *ClusterResourceModel) timeouts.Value { return m.Timeouts },
	// Provisioning typically takes 3-8 minutes; 15 covers a slow vCluster
	// start-up plus add-on reconcile headroom.
	defaults: operationTimeouts{
		create: defaultClusterCreateTimeout,
		update: defaultClusterUpdateTimeout,
		delete: defaultClusterDeleteTimeout,
	},
	ready: readiness[ClusterResourceModel, client.Cluster]{
		target:    clusterPhaseRunning,
		phase:     clusterPhase,
		converged: clusterConverged,
		failed: func(c *client.Cluster) (time.Duration, bool) {
			switch clusterPhase(c) {
			case phaseTerminating:
				return 0, true
			case clusterPhaseDegraded:
				// Only once the platform has seen the change, and then after
				// the grace: a cluster that was Degraded before it (a failed
				// deployment sync, which a spec change retries) must get the
				// chance to recover.
				return clusterDegradedGrace, c.Status.ObservedGeneration >= c.Generation
			}
			return 0, false
		},
		message: func(c *client.Cluster) string {
			if c == nil || c.Status == nil {
				return ""
			}
			return client.ConditionMessage(c.Status.Conditions, client.ConditionReady)
		},
		notApplied: func(c *client.Cluster) bool {
			return c != nil && c.Status != nil &&
				reconcileFailing(c.Generation, c.Status.ObservedGeneration, c.Status.Conditions)
		},
	},
	settingsChanged: clusterSettingsChanged,
	stillDeleting: func(m *ClusterResourceModel) string {
		return fmt.Sprintf("The platform accepted the delete, but cluster %q was still being removed when the "+
			"delete timeout ran out. Terraform has removed it from state. The platform carries on "+
			"tearing it down in the background, and the name stays reserved until it finishes, so "+
			"creating a cluster with the same name can fail with a conflict. If the cluster stays in "+
			"this state, check it in the Kupe console or contact Kupe Cloud support. For a cluster "+
			"that takes longer to tear down, set `timeouts.delete = \"30m\"`.", m.Name.ValueString())
	},
}

func (r *ClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (r *ClusterResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe cluster. Create and Update wait until the cluster is `Running` with the change " +
			"applied. They fail straight away if the cluster is being deleted (`Terminating`), and after 2 " +
			"minutes if it stays `Degraded` once the platform has picked up the change; a cluster that was " +
			"already `Degraded` gets the chance to recover with the change. If the timeout runs out first, the " +
			"apply fails while the platform carries on in the background, and a cluster that timed out on " +
			"Create stays in state, marked tainted. Delete waits until the cluster is gone. The timeouts " +
			"default to 15 minutes for create and update and 10 minutes for delete; set longer ones with " +
			"`timeouts`, for example `timeouts = { create = \"30m\" }`.\n\n" +
			"### What destroy deletes\n\n" +
			"Destroying the cluster, or removing it from the configuration, cannot be undone. The platform:\n\n" +
			"- deletes every workload in the cluster, with its storage;\n" +
			"- removes the cluster's public DNS endpoint.\n\n" +
			"The same happens whether you delete the cluster with Terraform, the `kupe` CLI or the " +
			"Kupe console.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Cluster name: a lowercase DNS label that starts with a letter and has no `--`. The tenant " +
					"and cluster names together can be at most 52 characters, and the platform refuses a longer " +
					"name when the cluster is created. Changing it replaces the cluster.",
				Required:   true,
				Validators: clusterNameValidators(),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"display_name": schema.StringAttribute{
				Description: "**Deprecated.** Clusters have no separate display name: `name` is shown everywhere, " +
					"including the console, the CLI and kubeconfig contexts. The API ignores this value, and the " +
					"provider keeps it in state exactly as configured. Remove it from your configuration. Before " +
					"provider v1.6.1 this attribute was required, and comparing it with the API's empty echo " +
					"tainted every new cluster with \"inconsistent result after apply\" and replaced it on the " +
					"next apply.",
				Optional: true,
				DeprecationMessage: "`display_name` is deprecated: the cluster `name` is the display name. The " +
					"attribute is ignored. Remove it from your configuration.",
			},
			"type": schema.StringAttribute{
				Description: "**Deprecated.** Cluster type. Only `shared` is supported, and it is the default; the platform " +
					"refuses `dedicated` with the error code `CLUSTER_DEDICATED_UNSUPPORTED`. Remove it from your " +
					"configuration. The attribute stays in the schema so that existing state still loads.",
				DeprecationMessage: "`type` is deprecated. Only `shared` is supported, and it is the default. Remove " +
					"the attribute from your configuration; a later provider release may drop it.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("shared"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("shared"),
				},
			},
			"version": schema.StringAttribute{
				Description: "Kubernetes version, for example `1.35`.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					// Keep the prior known value when config is null instead of
					// planning "known after apply" on every unrelated edit. This
					// suppresses noisy plan churn AND closes the TPK-1 hole where
					// an unknown version serialised to {"version":""} and could
					// trigger a silent K8s minor-version change on the server.
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"resources": schema.SingleNestedAttribute{
				Description: "CPU, memory and storage limits for the cluster. An update sends only the fields you " +
					"set here, and the platform keeps the current value of any field you leave out, so write " +
					"each field you want to change. Removing the whole `resources` block sends an empty " +
					"`resources` object, which asks the platform to clear the limits.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Description: "CPU limit, for example `4` or `500m`.",
						Optional:    true,
						Computed:    true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
						},
					},
					"memory": schema.StringAttribute{
						Description: "Memory limit, for example `16Gi` or `512Mi`.",
						Optional:    true,
						Computed:    true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
						},
					},
					"storage": schema.StringAttribute{
						Description: "Storage limit, for example `100Gi`.",
						Optional:    true,
						Computed:    true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
						},
					},
				},
			},
			"high_availability": schema.BoolAttribute{
				Description: "Runs a highly available control plane: 3 API server replicas and a separate 3-replica " +
					"etcd, kept on different nodes by hard anti-affinity. HA adds an hourly charge; " +
					"`data.kupe_plan` shows the rate. When " +
					"unset, a new cluster is created without HA and an existing or imported cluster keeps its " +
					"current setting, so removing the attribute does not turn HA off.\n\n" +
					"**Set at creation only.** Changing it on an existing cluster makes Terraform **replace** the " +
					"cluster (destroy, then create), because the platform refuses to turn HA on or off in place " +
					"(`HA_ENABLE_ON_EXISTING_UNSUPPORTED`, `HA_DISABLE_UNSUPPORTED`). To move to HA, create a new " +
					"HA cluster, redeploy your workloads to it, move the traffic across, then destroy the old one.",
				Optional: true,
				Computed: true,
				// No default: a schema default would apply to an unset
				// attribute even when state holds a value, so importing an
				// HA cluster would plan replacing it. Keeping the state value
				// first means only a written change replaces the cluster.
				// Create sends no value and the API defaults it to false.
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					boolplanmodifier.RequiresReplace(),
				},
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
				Description: "True once all 3 API server replicas and all 3 etcd replicas have been `Ready` for the first time. " +
					"etcd counts because it runs in its own pods: losing etcd quorum blocks writes even while the API " +
					"server pods are healthy. `high_availability` is the setting you asked for; read this attribute " +
					"when automation needs to wait until HA is working.",
				Computed: true,
			},
			"ha_enabled_at": schema.StringAttribute{
				Description: "When `ha_configured` first became true. HA is billed from this moment. It is set once " +
					"and never changed or cleared.",
				Computed: true,
			},
			"ha_phase": schema.StringAttribute{
				Description: "Summary of HA health: `pending`, `ha-healthy`, `ha-degraded` or `ha-unavailable`. Empty for " +
					"a cluster without HA. Use it in automation instead of reading individual conditions.",
				Computed: true,
			},
			"ha_replicas_ready": schema.Int64Attribute{
				Description: "Number of HA control-plane (API server) replicas that are `Ready`. Zero for a cluster without HA.",
				Computed:    true,
			},
			"ha_replicas_desired": schema.Int64Attribute{
				Description: "Target number of HA API server replicas: 3 when `high_availability = true`, otherwise 0.",
				Computed:    true,
			},
			"ha_etcd_replicas_ready": schema.Int64Attribute{
				Description: "Number of etcd replicas that are `Ready`. Reported separately because etcd runs in its own " +
					"pods: losing etcd quorum stops the cluster serving writes even while the API server replicas " +
					"are healthy.",
				Computed: true,
			},
			"ha_etcd_replicas_desired": schema.Int64Attribute{
				Description: "Target number of HA etcd replicas: 3 when `high_availability = true`, otherwise 0.",
				Computed:    true,
			},
			"etag":       etagAttribute(),
			"created_at": createdAtAttribute("the cluster"),
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
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
		createReq.Resources = resourcesFromPlan(plan.Resources)
	}

	cluster, etag, err := r.client.CreateCluster(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster", apiErrorDetail(err))
		return
	}

	// Surface server-side advisories (e.g. HA_K8S_VERSION_RETIRING) as
	// Terraform warnings. Advisory only — never fails the apply. We use
	// the canonical Code as the summary so users see the code in plan
	// output and can grep for it; the detail carries the human message
	// plus the field path if the server attached one.
	for _, w := range cluster.Warnings {
		detail := w.Message
		if w.Field != "" {
			detail = fmt.Sprintf("%s\n\nField: %s", w.Message, w.Field)
		}
		resp.Diagnostics.AddWarning(w.Code, detail)
	}

	// The API returns 201 the moment the cluster is accepted
	// (phase=Pending); the endpoint is populated and the vCluster
	// reachable a few minutes later. Without the wait, downstream
	// resources that reference kupe_cluster.foo.endpoint would get an
	// empty string at apply time.
	r.created(ctx, cluster, etag, &plan, &resp.State, &resp.Diagnostics)
}

func (r *ClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ClusterResourceModel
	var state ClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var send patchFunc[client.Cluster]
	if patch, hasChanges := clusterPatch(&plan, &state); hasChanges {
		name := plan.Name.ValueString()
		send = func(ctx context.Context, etag string) (*client.Cluster, string, error) {
			return r.client.UpdateCluster(ctx, name, etag, patch)
		}
	}
	// A version bump rolls the vCluster (Running, Upgrading, Running) and a
	// resources change is applied in place: either way the wait ends once
	// the cluster is Running at the post-PATCH generation.
	r.update(ctx, &plan, &state, state.ETag.ValueString(), send, true, &resp.State, &resp.Diagnostics)
}

// clusterPatch builds the merge patch for the fields that differ between
// plan and state.
func clusterPatch(plan, state *ClusterResourceModel) (client.PatchClusterRequest, bool) {
	patchReq := client.PatchClusterRequest{}
	hasChanges := false

	// Only send `version` when the planned value is KNOWN and actually
	// differs from prior state. `version` is Optional+Computed: when the
	// user leaves it unset and edits an unrelated attribute, the framework
	// can plan it as unknown ("known after apply"). An unknown value is not
	// Equal to the known prior state, so without the IsUnknown guard the
	// branch would fire and ValueString() would yield "" — serialising
	// `{"version":""}` (omitempty only drops nil pointers, not a pointer to
	// ""). kupe-api would then resolve "" to a default version and silently
	// upgrade/downgrade the tenant cluster's Kubernetes minor version on an
	// edit the user never made to version. The UseStateForUnknown plan
	// modifier on `version` normally keeps it known, but we guard here too
	// so the wire body can never carry an unintended version change.
	if !plan.Version.IsUnknown() && !plan.Version.Equal(state.Version) {
		v := plan.Version.ValueString()
		patchReq.Version = &v
		hasChanges = true
	}

	switch {
	case plan.Resources != nil && (state.Resources == nil ||
		!plan.Resources.CPU.Equal(state.Resources.CPU) ||
		!plan.Resources.Memory.Equal(state.Resources.Memory) ||
		!plan.Resources.Storage.Equal(state.Resources.Storage)):
		patchReq.Resources = resourcesFromPlan(plan.Resources)
		hasChanges = true
	case plan.Resources == nil && state.Resources != nil:
		// User removed the resources block — send empty to clear.
		patchReq.Resources = &client.ResourceQuantities{}
		hasChanges = true
	}

	// HA changes are handled by RequiresReplace on the high_availability
	// attribute — Terraform will destroy + recreate the cluster rather
	// than Update, so this never sees an HA diff.
	return patchReq, hasChanges
}

// clusterPhase is c's phase, "" when it has no status yet.
func clusterPhase(c *client.Cluster) string {
	if c == nil || c.Status == nil {
		return ""
	}
	return c.Status.Phase
}

// clusterConverged reports whether the cluster is Running with its latest
// spec applied: observedGeneration has reached generation. A resources
// change is applied in place and never leaves Running, so the phase alone
// would end the wait before the platform saw the change. A kupe-api that
// sends no generation (0) leaves the phase as the only signal.
func clusterConverged(c *client.Cluster) bool {
	return c.Status != nil &&
		c.Status.Phase == clusterPhaseRunning &&
		c.Status.ObservedGeneration >= c.Generation
}

// clusterSettingsChanged lists, by attribute name, the settings an update
// can change whose value in current differs from prior.
func clusterSettingsChanged(prior *ClusterResourceModel, current *client.Cluster) []string {
	var changed []string
	if !types.StringValue(current.Version).Equal(prior.Version) {
		changed = append(changed, "version")
	}
	if !resourcesEqual(nonEmptyResources(prior.Resources), clusterResourcesFromAPI(current)) {
		changed = append(changed, "resources")
	}
	if !types.BoolValue(current.HighAvailability).Equal(prior.HighAvailability) {
		changed = append(changed, "high_availability")
	}
	return changed
}

// clusterResourcesFromAPI is the cluster's limits, nil when it has none.
func clusterResourcesFromAPI(c *client.Cluster) *ResourcesModel {
	if c.Resources == nil || (c.Resources.CPU == "" && c.Resources.Memory == "" && c.Resources.Storage == "") {
		return nil
	}
	return &ResourcesModel{
		CPU:     stringOrNull(c.Resources.CPU),
		Memory:  stringOrNull(c.Resources.Memory),
		Storage: stringOrNull(c.Resources.Storage),
	}
}

// nonEmptyResources is m, or nil when m sets no limit: an empty
// `resources = {}` block and no block both mean no limits.
func nonEmptyResources(m *ResourcesModel) *ResourcesModel {
	if m == nil || (m.CPU.IsNull() && m.Memory.IsNull() && m.Storage.IsNull()) {
		return nil
	}
	return m
}

func mapClusterToState(c *client.Cluster, etag string, state *ClusterResourceModel) {
	state.Name = types.StringValue(c.Name)
	// DisplayName is deliberately NOT mapped from the API: the attribute is
	// deprecated/ignored and the API mirrors `name` in the response. The
	// configured value (or null) is preserved so plans stay clean.
	state.Type = types.StringValue(c.Type)
	state.Version = types.StringValue(c.Version)
	state.HighAvailability = types.BoolValue(c.HighAvailability)
	state.ETag = types.StringValue(etag)
	state.CreatedAt = types.StringValue(c.CreatedAt)

	state.clusterStatusModel = clusterStatusFromAPI(c.Status)

	// state.Resources still holds the planned (Create/Update) or prior
	// (Read) block at this point — this is the last field we map. Use it
	// to disambiguate "user never set a resources block" from "user wrote
	// `resources = {}` to clear the limits", because the server echoes an
	// empty `{}` for both.
	switch limits := clusterResourcesFromAPI(c); {
	case limits != nil:
		state.Resources = limits
	case state.Resources != nil:
		// The server reports no limits, but the plan/prior state carried a
		// (now-empty) `resources` block. Collapsing to nil here would turn
		// a known object into null and fail "Provider produced inconsistent
		// result after apply" (and show perpetual drift on refresh).
		// Preserve an all-null block matching the planned empty shape.
		state.Resources = &ResourcesModel{
			CPU:     types.StringNull(),
			Memory:  types.StringNull(),
			Storage: types.StringNull(),
		}
	default:
		state.Resources = nil
	}
}

// clusterStatusFromAPI maps a cluster's status; no status yet reads as
// empty values.
func clusterStatusFromAPI(status *client.ClusterStatus) clusterStatusModel {
	if status == nil {
		status = &client.ClusterStatus{}
	}
	return clusterStatusModel{
		Phase:                 types.StringValue(status.Phase),
		Endpoint:              types.StringValue(status.Endpoint),
		HAConfigured:          types.BoolValue(status.HAConfigured),
		HAEnabledAt:           types.StringValue(status.HAEnabledAt),
		HAPhase:               types.StringValue(status.HAPhase),
		HAReplicasReady:       types.Int64Value(int64(status.HAReplicasReady)),
		HAReplicasDesired:     types.Int64Value(int64(status.HAReplicasDesired)),
		HAEtcdReplicasReady:   types.Int64Value(int64(status.HAEtcdReplicasReady)),
		HAEtcdReplicasDesired: types.Int64Value(int64(status.HAEtcdReplicasDesired)),
	}
}
