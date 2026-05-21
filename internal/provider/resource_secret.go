package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Secret phase the provider considers "ready" — Create and Update wait
// for this value before returning. Other observed phases (Pending,
// Degraded) are treated as in-progress until either Active is reached
// or the user-configured Create/Update timeout fires.
const secretPhaseActive = "Active"

// Default timeouts for secret operations. Users can override via the
// resource's `timeouts` block. Sync to target clusters typically
// completes in seconds; 2 min is comfortable headroom.
const (
	defaultSecretCreateTimeout = 2 * time.Minute
	defaultSecretUpdateTimeout = 2 * time.Minute
	defaultSecretDeleteTimeout = 2 * time.Minute
)

var (
	_ resource.Resource                = &SecretResource{}
	_ resource.ResourceWithImportState = &SecretResource{}
)

type SecretResource struct {
	client *client.Client
}

type SecretResourceModel struct {
	Name       types.String   `tfsdk:"name"`
	SecretPath types.String   `tfsdk:"secret_path"`
	Sync       types.List     `tfsdk:"sync"`
	Phase      types.String   `tfsdk:"phase"`
	ETag       types.String   `tfsdk:"etag"`
	CreatedAt  types.String   `tfsdk:"created_at"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

var syncTargetAttrTypes = map[string]attr.Type{
	"cluster":     types.StringType,
	"namespace":   types.StringType,
	"secret_name": types.StringType,
}

func NewSecretResource() resource.Resource {
	return &SecretResource{}
}

func (r *SecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *SecretResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe Cloud secret definition and its sync targets. Create and " +
			"Update wait for the secret to reach phase=Active before returning; Delete waits for " +
			"the underlying ManagedSecret CR to finish terminating. Timeouts are configurable via " +
			"the `timeouts` block (defaults: create/update/delete 2m).",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Secret name (immutable after creation).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"secret_path": schema.StringAttribute{
				Description: "OpenBao KV v2 key path for the stored value. Immutable after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"sync": schema.ListNestedAttribute{
				Description: "Cluster/namespace targets to sync this secret to.",
				Optional:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"cluster": schema.StringAttribute{
							Description: "Target cluster name.",
							Required:    true,
						},
						"namespace": schema.StringAttribute{
							Description: "Target namespace in the cluster.",
							Required:    true,
						},
						"secret_name": schema.StringAttribute{
							Description: "Override the K8s secret name (defaults to the managed secret name).",
							Optional:    true,
						},
					},
				},
			},
			"phase": schema.StringAttribute{
				Description: "Current sync phase for the managed secret.",
				Computed:    true,
			},
			"etag": schema.StringAttribute{
				// etag intentionally has no UseStateForUnknown: it changes on
				// every PATCH and we want the plan to honestly say "known
				// after apply" on any update.
				Description: "Resource version used for optimistic locking during updates.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the managed secret definition was created.",
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

func (r *SecretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *SecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := client.CreateSecretRequest{
		Name:       plan.Name.ValueString(),
		SecretPath: plan.SecretPath.ValueString(),
		Sync:       extractSyncTargets(plan.Sync),
	}

	secret, etag, err := r.client.CreateSecret(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create secret", apiErrorDetail(err))
		return
	}

	mapSecretToState(secret, etag, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Wait for the operator to finish syncing to all target clusters
	// before returning. The API returns 201 when the ManagedSecret CR
	// is accepted (phase=Pending); the actual sync happens via
	// ExternalSecrets and can take a few seconds per target cluster.
	// User-overridable via `timeouts.create` (default 2m).
	timeout, diags := plan.Timeouts.Create(ctx, defaultSecretCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitForSecretReady(ctx, timeout, plan.Name.ValueString(), &plan, &resp.State, &resp.Diagnostics, "create")
}

func (r *SecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	secret, etag, err := r.client.GetSecret(ctx, state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read secret", apiErrorDetail(err))
		return
	}

	mapSecretToState(secret, etag, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	patchReq := client.PatchSecretRequest{
		Sync: extractSyncTargets(plan.Sync),
	}

	secret, etag, err := r.client.UpdateSecret(ctx, plan.Name.ValueString(), state.ETag.ValueString(), patchReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to update secret", apiErrorDetail(err))
		return
	}

	mapSecretToState(secret, etag, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Sync-target changes briefly transition phase Active → Pending →
	// Active as the operator reconciles. Wait for the second Active
	// observation so downstream resources see fully-synced state.
	// User-overridable via `timeouts.update` (default 2m).
	timeout, diags := plan.Timeouts.Update(ctx, defaultSecretUpdateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitForSecretReady(ctx, timeout, plan.Name.ValueString(), &plan, &resp.State, &resp.Diagnostics, "update")
}

func (r *SecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := state.Name.ValueString()
	if err := r.client.DeleteSecret(ctx, name); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to delete secret", apiErrorDetail(err))
		return
	}

	// Poll until the ManagedSecret CR is genuinely gone. kupe-api's
	// DELETE returns 204 once K8s accepts the request, but the
	// operator's finalizer cleans up synced K8s Secrets across all
	// target clusters before allowing the CR to be removed. Without
	// this wait, an immediate re-apply with the same secret name 409s
	// on the still-terminating CR. User-overridable via
	// `timeouts.delete` (default 2m).
	timeout, diags := state.Timeouts.Delete(ctx, defaultSecretDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := waitForCondition(waitCtx, func(c context.Context) (bool, error) {
		_, _, err := r.client.GetSecret(c, name)
		return client.IsNotFound(err), nil
	})
	if err != nil {
		resp.Diagnostics.AddWarning(
			"secret still terminating",
			fmt.Sprintf("DELETE was accepted but secret %q is still terminating after the configured "+
				"delete timeout. Kupe Cloud is still cleaning up the synced Secrets in your target "+
				"clusters. Re-running `terraform destroy` will wait again, and the same secret name "+
				"cannot be reused until termination completes. If the secret stays in this state, "+
				"check it in the Kupe Console or contact Kupe Cloud support. Override "+
				"with `timeouts.delete = \"5m\"` for secrets with many sync targets.", name),
		)
	}
}

func (r *SecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// waitForSecretReady polls GetSecret until the observed phase is
// "Active" or the context expires. Refreshes resource state on every
// poll so an interrupted apply leaves Terraform with the latest
// observed values. kind is "create" or "update", surfaced in the
// warning so users can distinguish the two paths. See the
// corresponding cluster helper for the design rationale.
func (r *SecretResource) waitForSecretReady(
	ctx context.Context,
	timeout time.Duration,
	name string,
	plan *SecretResourceModel,
	state *tfsdk.State,
	diags *diag.Diagnostics,
	kind string,
) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := waitForCondition(waitCtx, func(c context.Context) (bool, error) {
		secret, etag, err := r.client.GetSecret(c, name)
		if err != nil {
			// Transient — keep polling. See the cluster helper for
			// the same rationale and waitForCondition's contract.
			return false, nil //nolint:nilerr // transient errors keep the poll going
		}
		mapSecretToState(secret, etag, plan, diags)
		diags.Append(state.Set(c, plan)...)
		phase := ""
		if secret.Status != nil {
			phase = secret.Status.Phase
		}
		return phase == secretPhaseActive, nil
	})
	if err != nil {
		diags.AddWarning(
			fmt.Sprintf("secret %s timed out before reaching Active", kind),
			fmt.Sprintf("secret %q reached phase=%q before the %s timeout fired. Wait a few moments "+
				"and re-run `terraform apply` to pick up the Active state — Kupe Cloud is still "+
				"syncing the value to your target clusters and a subsequent apply will be a no-op "+
				"once the sync completes. If the secret stays in this phase for longer than expected, "+
				"check it in the Kupe Console or contact Kupe Cloud support. Override "+
				"with `timeouts.%s = \"5m\"` for secrets with many sync targets.",
				name, plan.Phase.ValueString(), kind, kind),
		)
	}
}

func extractSyncTargets(list types.List) []client.SyncTarget {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	// Return a non-nil empty slice for sync = [] so the API receives
	// "sync": [] rather than omitting the field — this lets the user
	// explicitly clear all sync targets.
	targets := make([]client.SyncTarget, 0, len(list.Elements()))
	for _, elem := range list.Elements() {
		obj, ok := elem.(types.Object)
		if !ok {
			continue
		}
		attrs := obj.Attributes()
		cluster, _ := attrs["cluster"].(types.String)
		namespace, _ := attrs["namespace"].(types.String)
		t := client.SyncTarget{
			Cluster:   cluster.ValueString(),
			Namespace: namespace.ValueString(),
		}
		if sn, ok := attrs["secret_name"].(types.String); ok && !sn.IsNull() {
			t.SecretName = sn.ValueString()
		}
		targets = append(targets, t)
	}
	return targets
}

func mapSecretToState(s *client.Secret, etag string, state *SecretResourceModel, diags *diag.Diagnostics) {
	state.Name = types.StringValue(s.Name)
	state.SecretPath = types.StringValue(s.SecretPath)
	state.ETag = types.StringValue(etag)
	state.CreatedAt = types.StringValue(s.CreatedAt)

	if s.Status != nil {
		state.Phase = types.StringValue(s.Status.Phase)
	} else {
		state.Phase = types.StringValue("")
	}

	// Distinguish null (field absent from API) from empty (user explicitly
	// set sync = []). The API returns nil/absent when no sync has ever been
	// set, but returns an empty array when the user cleared it. Mapping both
	// to ListNull would cause perpetual diffs for sync = [] configs.
	if s.Sync == nil {
		state.Sync = types.ListNull(types.ObjectType{AttrTypes: syncTargetAttrTypes})
	} else {
		syncElements := make([]attr.Value, 0, len(s.Sync))
		for _, t := range s.Sync {
			secretName := types.StringNull()
			if t.SecretName != "" {
				secretName = types.StringValue(t.SecretName)
			}
			attrs := map[string]attr.Value{
				"cluster":     types.StringValue(t.Cluster),
				"namespace":   types.StringValue(t.Namespace),
				"secret_name": secretName,
			}
			objVal, d := types.ObjectValue(syncTargetAttrTypes, attrs)
			diags.Append(d...)
			syncElements = append(syncElements, objVal)
		}
		listVal, d := types.ListValue(types.ObjectType{AttrTypes: syncTargetAttrTypes}, syncElements)
		diags.Append(d...)
		state.Sync = listVal
	}
}
