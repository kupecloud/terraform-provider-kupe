package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Registry phases Create and Update act on. Running is the one they wait
// for; Pending and Provisioning are in progress; Terminating ends the wait at
// once and Degraded, once the platform has seen the change, after
// registryDegradedGrace.
const (
	registryPhaseRunning  = "Running"
	registryPhaseDegraded = "Degraded"
)

// registryDegradedGrace is how long Create and Update ride out a Degraded
// registry that has seen the change before failing: a registry is Degraded
// while the platform cannot reach the image store, which a short outage
// clears. A var so tests can shorten it.
var registryDegradedGrace = 2 * time.Minute

// registryDeleteNotice is what deleting a registry means for its images.
// The API docs, the CLI, these docs and the diagnostics use the same words.
const registryDeleteNotice = "Deleting a registry deletes every image in it. They cannot be restored."

// Default timeouts for registry operations, overridable in the `timeouts`
// block. Provisioning sets up storage and access in seconds; deleting removes
// every image, which takes longer for a full registry.
const (
	defaultRegistryCreateTimeout = 10 * time.Minute
	defaultRegistryUpdateTimeout = 5 * time.Minute
	defaultRegistryDeleteTimeout = 15 * time.Minute
)

var (
	_ resource.Resource                = &RegistryResource{}
	_ resource.ResourceWithConfigure   = &RegistryResource{}
	_ resource.ResourceWithImportState = &RegistryResource{}
)

// RegistryResource is kupe_registry. Read, Delete and Configure come from
// managedResource.
type RegistryResource struct {
	managedResource[RegistryResourceModel, client.Registry]
}

// registryModel holds the attributes the kupe_registry resource and data
// source share.
type registryModel struct {
	Name               types.String `tfsdk:"name"`
	StorageLimit       types.String `tfsdk:"storage_limit"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
	Endpoint           types.String `tfsdk:"endpoint"`
	StorageUsedBytes   types.Int64  `tfsdk:"storage_used_bytes"`
	StorageLimitBytes  types.Int64  `tfsdk:"storage_limit_bytes"`
	RepositoryCount    types.Int64  `tfsdk:"repository_count"`
	Phase              types.String `tfsdk:"phase"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

type RegistryResourceModel struct {
	registryModel
	ETag     types.String   `tfsdk:"etag"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func NewRegistryResource() resource.Resource {
	return &RegistryResource{managedResource[RegistryResourceModel, client.Registry]{kind: &registryKind}}
}

// registryKind is the registry's lifecycle for managedResource.
var registryKind = serviceKind[RegistryResourceModel, client.Registry]{
	noun: "registry",
	name: func(m *RegistryResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *RegistryResourceModel) (*client.Registry, string, error) {
		return c.GetRegistry(ctx, m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *RegistryResourceModel) error {
		return c.DeleteRegistry(ctx, m.Name.ValueString())
	},
	mapToState: mapRegistryToState,
	timeoutsOf: func(m *RegistryResourceModel) timeouts.Value { return m.Timeouts },
	defaults: operationTimeouts{
		create: defaultRegistryCreateTimeout,
		update: defaultRegistryUpdateTimeout,
		delete: defaultRegistryDeleteTimeout,
	},
	ready: readiness[RegistryResourceModel, client.Registry]{
		target:    registryPhaseRunning,
		phase:     registryPhase,
		converged: registryConverged,
		failed: func(reg *client.Registry) (time.Duration, bool) {
			switch registryPhase(reg) {
			case phaseTerminating:
				return 0, true
			case registryPhaseDegraded:
				// Degraded from before the change is not a verdict on it.
				return registryDegradedGrace, reg.Status.ObservedGeneration >= reg.Generation
			}
			return 0, false
		},
		message: func(reg *client.Registry) string {
			if reg == nil || reg.Status == nil {
				return ""
			}
			return client.ConditionMessage(reg.Status.Conditions, client.ConditionReady)
		},
		notApplied: func(reg *client.Registry) bool {
			return reg != nil && reg.Status != nil &&
				reconcileFailing(reg.Generation, reg.Status.ObservedGeneration, reg.Status.Conditions)
		},
	},
	settingsChanged: func(prior *RegistryResourceModel, current *client.Registry) []string {
		now := registryModelFromAPI(current)
		var changed []string
		if !now.StorageLimit.Equal(prior.StorageLimit) {
			changed = append(changed, "storage_limit")
		}
		if !now.DeletionProtection.Equal(prior.DeletionProtection) {
			changed = append(changed, "deletion_protection")
		}
		return changed
	},
	deleteRefused: func(m *RegistryResourceModel, err error) (string, string, bool) {
		if !client.IsDeletionProtected(err) {
			return "", "", false
		}
		return "registry has deletion protection enabled",
			fmt.Sprintf("Registry %q was not deleted because deletion protection is enabled. To delete it, set "+
				"`deletion_protection = false` on the resource, run `terraform apply`, then destroy it again. %s",
				m.Name.ValueString(), registryDeleteNotice), true
	},
	stillDeleting: func(m *RegistryResourceModel) string {
		return fmt.Sprintf("DELETE was accepted but registry %q is still being deleted after the configured delete "+
			"timeout. Deleting a registry deletes every image in it; this can take a few minutes. Terraform has "+
			"removed it from state; the Kupe platform finishes in the background, and a new registry cannot be "+
			"created until it is gone. Override with `timeouts.delete = \"30m\"`.", m.Name.ValueString())
	},
}

func (r *RegistryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry"
}

func (r *RegistryResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the tenant's Kupe container registry. A tenant has one registry, always named " +
			"`registry`; images live at `registry.kupe.cloud/<tenant>/<repository>:<tag>`. Create and Update wait " +
			"for the registry to reach phase=Running with the change in effect. They fail at once if the registry " +
			"is being deleted (Terminating), and after 2 minutes if it stays Degraded once the platform has picked " +
			"up the change. If the timeout fires first, the apply fails; the platform carries on in the background, " +
			"and a registry that timed out on Create stays in state, marked tainted. A change to " +
			"`deletion_protection` alone takes effect at once and is not waited for. Delete waits until the " +
			"registry is gone. Set timeouts with the `timeouts` attribute, for example " +
			"`timeouts = { create = \"20m\" }` (defaults: create 10m, update 5m, delete 15m).\n\n" +
			"Clusters pull from the registry through `kupe_registry_binding`, and CI systems sign in with a " +
			"`kupe_registry_token`.\n\n" +
			"### Destroy semantics\n\n" +
			registryDeleteNotice + " `deletion_protection` (default `true`) makes the Kupe platform refuse the " +
			"delete: set it to `false` and apply before destroying. Add `lifecycle { prevent_destroy = true }` to " +
			"stop Terraform from planning the destroy at all.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Registry name. A tenant has one registry, named `registry`: this is the default and " +
					"the only value accepted.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString(registryName),
				Validators: registryNameValidators(),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"storage_limit": schema.StringAttribute{
				Description: "Most the registry may store, with a binary unit (`Mi`, `Gi` or `Ti`), e.g. `10Gi`. It " +
					"can only be set at or below the most your plan allows. When unset, the plan's limit applies. " +
					"Changes in place; lowering it below what the registry stores is accepted, and pushes are then " +
					"refused until images are deleted.",
				Optional:   true,
				Validators: registryStorageLimitValidators(),
			},
			"deletion_protection": schema.BoolAttribute{
				Description: "While `true`, the Kupe platform refuses to delete the registry. Set it to `false` and " +
					"apply before destroying. Default `true`, also for an existing registry: importing an unprotected " +
					"registry without this attribute in configuration plans turning protection on.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"endpoint": schema.StringAttribute{
				Description: "Image path prefix to push to and pull from, e.g. `registry.kupe.cloud/acme`. Empty " +
					"until the registry is provisioned.",
				Computed: true,
			},
			"storage_used_bytes": schema.Int64Attribute{
				Description: "What the registry stores, in bytes, as last measured by the platform.",
				Computed:    true,
			},
			"storage_limit_bytes": schema.Int64Attribute{
				Description: "The storage limit in force, in bytes: `storage_limit` when set, else the plan's. " +
					"`-1` means unlimited. Null until the platform has applied a limit.",
				Computed: true,
			},
			"repository_count": schema.Int64Attribute{
				Description: "Number of repositories in the registry.",
				Computed:    true,
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending, Provisioning, Running, Degraded or Terminating.",
				Computed:    true,
			},
			"etag":       etagAttribute(),
			"created_at": createdAtAttribute("the registry"),
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

func (r *RegistryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RegistryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deletionProtection := plan.DeletionProtection.ValueBool()
	reg, etag, err := r.client.CreateRegistry(ctx, client.CreateRegistryRequest{
		Name:               plan.Name.ValueString(),
		StorageLimit:       plan.StorageLimit.ValueString(),
		DeletionProtection: &deletionProtection,
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create registry", apiErrorDetail(err))
		return
	}
	r.created(ctx, reg, etag, &plan, &resp.State, &resp.Diagnostics)
}

func (r *RegistryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RegistryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var send patchFunc[client.Registry]
	patch, hasChanges := registryPatch(&plan, &state)
	if hasChanges {
		name := plan.Name.ValueString()
		send = func(ctx context.Context, etag string) (*client.Registry, string, error) {
			return r.client.UpdateRegistry(ctx, name, etag, patch)
		}
	}
	// Deletion protection is read from the spec when a delete arrives, so it
	// is in effect as soon as the PATCH is accepted: not waited for.
	protectionOnly := patch.DeletionProtection != nil && patch.StorageLimit == nil
	r.update(ctx, &plan, &state, state.ETag.ValueString(), send, !protectionOnly, &resp.State, &resp.Diagnostics)
}

// ImportState accepts "registry", the registry's one name.
func (r *RegistryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != registryName {
		resp.Diagnostics.AddError("invalid import ID",
			fmt.Sprintf("a tenant has one registry, named registry: import it with the ID %q, got %q", registryName, req.ID))
		return
	}
	r.managedResource.ImportState(ctx, req, resp)
}

// registryPatch builds the merge patch for the settings that differ between
// plan and state. A storage_limit removed from configuration is sent as "",
// which removes the limit so the plan's applies.
func registryPatch(plan, state *RegistryResourceModel) (client.PatchRegistryRequest, bool) {
	var patch client.PatchRegistryRequest
	hasChanges := false
	if !plan.StorageLimit.Equal(state.StorageLimit) {
		limit := plan.StorageLimit.ValueString()
		patch.StorageLimit = &limit
		hasChanges = true
	}
	if !plan.DeletionProtection.Equal(state.DeletionProtection) {
		protected := plan.DeletionProtection.ValueBool()
		patch.DeletionProtection = &protected
		hasChanges = true
	}
	return patch, hasChanges
}

// registryConverged reports whether the registry is Running with its latest
// spec applied.
func registryConverged(reg *client.Registry) bool {
	return reg.Status != nil && reg.Status.Phase == registryPhaseRunning &&
		reg.Status.ObservedGeneration >= reg.Generation
}

// registryPhase is reg's phase, "" when it has no status yet.
func registryPhase(reg *client.Registry) string {
	if reg == nil || reg.Status == nil {
		return ""
	}
	return reg.Status.Phase
}

func mapRegistryToState(reg *client.Registry, etag string, state *RegistryResourceModel) {
	state.registryModel = registryModelFromAPI(reg)
	state.ETag = types.StringValue(etag)
}

func registryModelFromAPI(reg *client.Registry) registryModel {
	m := registryModel{
		Name:               types.StringValue(reg.Name),
		StorageLimit:       stringOrNull(reg.StorageLimit),
		DeletionProtection: types.BoolValue(reg.DeletionProtection),
		Endpoint:           types.StringValue(""),
		StorageUsedBytes:   types.Int64Value(0),
		StorageLimitBytes:  types.Int64Null(),
		RepositoryCount:    types.Int64Value(0),
		Phase:              types.StringValue(""),
		CreatedAt:          types.StringValue(reg.CreatedAt),
	}
	if s := reg.Status; s != nil {
		m.Endpoint = types.StringValue(s.Endpoint)
		m.StorageUsedBytes = types.Int64Value(s.StorageUsedBytes)
		m.StorageLimitBytes = types.Int64PointerValue(s.StorageLimitBytes)
		m.RepositoryCount = types.Int64Value(s.RepositoryCount)
		m.Phase = types.StringValue(s.Phase)
	}
	return m
}
