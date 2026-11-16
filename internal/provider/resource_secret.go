package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const (
	secretPhaseActive          = "Active"
	secretPhaseDeleting        = "Deleting"
	defaultSecretCreateTimeout = 2 * time.Minute
	defaultSecretUpdateTimeout = 2 * time.Minute
	defaultSecretDeleteTimeout = 2 * time.Minute
	maxSecretKeysPerTarget     = 64
	maxSecretKeys              = 1024
)

var (
	_ resource.Resource                     = &SecretResource{}
	_ resource.ResourceWithConfigure        = &SecretResource{}
	_ resource.ResourceWithImportState      = &SecretResource{}
	_ resource.ResourceWithValidateConfig   = &SecretResource{}
	_ resource.ResourceWithConfigValidators = &SecretResource{}
)

type SecretResource struct {
	managedResource[SecretResourceModel, client.Secret]
}

type SecretResourceModel struct {
	ValuesWO        types.Map      `tfsdk:"values_wo"`
	ValuesWOVersion types.Int64    `tfsdk:"values_wo_version"`
	SourceVersion   types.Int64    `tfsdk:"source_version"`
	Name            types.String   `tfsdk:"name"`
	SecretPath      types.String   `tfsdk:"secret_path"`
	Sync            types.List     `tfsdk:"sync"`
	SyncStatus      types.List     `tfsdk:"sync_status"`
	Phase           types.String   `tfsdk:"phase"`
	ETag            types.String   `tfsdk:"etag"`
	CreatedAt       types.String   `tfsdk:"created_at"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

var syncTargetAttrTypes = map[string]attr.Type{
	"cluster":     types.StringType,
	"namespace":   types.StringType,
	"secret_name": types.StringType,
	"keys":        types.SetType{ElemType: types.StringType},
}

var secretTargetStatusType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"cluster":     types.StringType,
	"namespace":   types.StringType,
	"secret_name": types.StringType,
	"phase":       types.StringType,
	"reason":      types.StringType,
}}

func NewSecretResource() resource.Resource {
	return &SecretResource{managedResource[SecretResourceModel, client.Secret]{kind: &secretKind}}
}

var secretKind = serviceKind[SecretResourceModel, client.Secret]{
	noun: "secret",
	name: func(m *SecretResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *SecretResourceModel) (*client.Secret, string, error) {
		return c.GetSecret(ctx, m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *SecretResourceModel) error {
		return c.DeleteSecret(ctx, m.Name.ValueString())
	},
	mapToState: mapSecretToState,
	timeoutsOf: func(m *SecretResourceModel) timeouts.Value { return m.Timeouts },
	defaults:   operationTimeouts{create: defaultSecretCreateTimeout, update: defaultSecretUpdateTimeout, delete: defaultSecretDeleteTimeout},
	ready: readiness[SecretResourceModel, client.Secret]{
		target:    "delivered",
		phase:     secretPhase,
		converged: secretDelivered,
		failed: func(s *client.Secret) (time.Duration, bool) {
			return 0, secretPhase(s) == secretPhaseDeleting || len(secretConflicts(s)) > 0
		},
		failedDetail: secretFailedDetail,
		timedOutDetail: func(_ *SecretResourceModel, _ *client.Secret) string {
			return " Delivery waits for the current generation, every target to be Synced, and removed targets to be withdrawn."
		},
		// The controller observes every pass, including ProviderError; it
		// does not publish the ReconcileFailed condition used by notApplied.
	},
	settingsChanged: func(prior *SecretResourceModel, current *client.Secret) []string {
		if secretTargetsEqual(extractSyncTargets(prior.Sync), current.Sync) {
			return nil
		}
		return []string{"sync"}
	},
	stillDeleting: func(m *SecretResourceModel) string {
		return fmt.Sprintf("The platform accepted the delete, but secret %q was still being removed when the delete timeout ran out. "+
			"Terraform has removed it from state. The platform finishes withdrawing the copies in the background, "+
			"and the name stays reserved until then, so creating a secret with the same name can fail with a conflict. "+
			"The copy inside the cluster goes shortly after, or later if the cluster is down. Stored values are kept.", m.Name.ValueString())
	},
}

func secretPhase(s *client.Secret) string {
	if s == nil || s.Status == nil {
		return ""
	}
	return s.Status.Phase
}

// secretTargetMatches includes the effective name: a retained namespace may
// still report delivery or a conflict for the name used before a rename.
func secretTargetMatches(name string, target client.SyncTarget, status client.SecretTargetStatus) bool {
	if target.SecretName != "" {
		name = target.SecretName
	}
	return target.Cluster == status.Cluster && target.Namespace == status.Namespace && name == status.SecretName
}

func secretDelivered(s *client.Secret) bool {
	if secretPhase(s) != secretPhaseActive || s.Generation < 1 || s.Status.ObservedGeneration != s.Generation {
		return false
	}
	for _, status := range s.Status.SyncStatuses {
		if status.Phase == "Withdrawing" {
			return false
		}
	}
	for _, target := range s.Sync {
		if !slices.ContainsFunc(s.Status.SyncStatuses, func(status client.SecretTargetStatus) bool {
			return secretTargetMatches(s.Name, target, status) && status.Phase == "Synced"
		}) {
			return false
		}
	}
	return true
}

func secretConflicts(s *client.Secret) []string {
	if s == nil || s.Status == nil || s.Generation < 1 || s.Status.ObservedGeneration != s.Generation {
		return nil
	}
	var conflicts []string
	for _, target := range s.Sync {
		for _, status := range s.Status.SyncStatuses {
			if secretTargetMatches(s.Name, target, status) && status.Phase == "Failed" && status.Reason == "TargetConflict" {
				conflicts = append(conflicts, fmt.Sprintf("%s/%s (TargetConflict)", target.Cluster, target.Namespace))
			}
		}
	}
	slices.Sort(conflicts)
	return conflicts
}

func secretFailedDetail(op string, m *SecretResourceModel, s *client.Secret) string {
	if secretPhase(s) == secretPhaseDeleting {
		return fmt.Sprintf("Secret %q is being deleted, so the %s did not complete. Once it is gone, "+
			"`terraform apply` creates it again; remove it from the configuration if that is not wanted.", m.Name.ValueString(), op)
	}
	detail := fmt.Sprintf("Secret %q could not deliver to %s, so the %s did not complete. "+
		"A Secret with this name already exists in the namespace and was not created by Kupe. "+
		"Choose another `secret_name` for the target.", m.Name.ValueString(), strings.Join(secretConflicts(s), ", "), op)
	if op == "create" {
		return detail + "\n\nTerraform keeps the secret in state, marked tainted, so the next `terraform apply` plans to replace it. " +
			"To keep it instead, run `terraform untaint` on it."
	}
	return detail + "\n\n" + changeRecordedNotice
}

func (r *SecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *SecretResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe Cloud secret: a Vault path whose values Kupe delivers as Kubernetes Secrets to the cluster namespaces you list. " +
			"Write values with `values_wo` and `values_wo_version`, which need OpenTofu 1.11 or Terraform 1.11.1 or later. " +
			"The values never reach plan or state, and destroying the secret keeps them in Vault. " +
			"Create and Update wait until every target has the current values and removed targets are withdrawn; the timeouts default to 2 minutes.",
		Attributes: map[string]schema.Attribute{
			"values_wo": schema.MapAttribute{
				Description: "Complete set of values to store, replacing what is there. Write-only and sensitive, so never stored in plan or state. Set it with `values_wo_version` and change that number for each write. Removing both keeps the stored values.",
				ElementType: types.StringType, Optional: true, Sensitive: true, WriteOnly: true,
				Validators: []validator.Map{mapvalidator.SizeAtLeast(1), mapvalidator.KeysAre(configMapKey{}), mapvalidator.NoNullValues()},
			},
			"values_wo_version": schema.Int64Attribute{
				Description: "Positive number that controls when `values_wo` is sent. Change it to replace the stored values. While it stays the same, nothing is sent, even if `values_wo` changes.",
				Optional:    true, Validators: []validator.Int64{int64validator.AtLeast(1)}, // codespell:ignore atleast
			},
			"source_version": schema.Int64Attribute{
				Description: "Version of the Vault entry, read from its metadata, or null when there is no entry. A value write checks the version shown in the plan and refuses to overwrite a change made since.",
				Computed:    true, PlanModifiers: []planmodifier.Int64{secretSourceVersionModifier{}},
			},
			"name": schema.StringAttribute{Description: "Secret name. Changing it replaces the secret.", Required: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"secret_path": schema.StringAttribute{Description: "Vault path of the stored values. Changing it replaces the secret.", Required: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"sync": schema.ListNestedAttribute{
				Description: "Cluster namespaces to deliver the values to. Changes apply in place. All targets together can select at most 1,024 keys.",
				Optional:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"cluster":     schema.StringAttribute{Description: "Target cluster name.", Required: true},
					"namespace":   schema.StringAttribute{Description: "Target namespace in the cluster.", Required: true},
					"secret_name": schema.StringAttribute{Description: "Name of the Kubernetes Secret in the namespace. Defaults to the secret's `name`.", Optional: true},
					"keys": schema.SetAttribute{
						Description: "Keys to deliver to this target, 1 to 64. Each key is 1 to 253 ASCII letters, digits, `-`, `_` or `.`, and cannot be `.` or start with `..`. Omit it to deliver the whole entry.",
						ElementType: types.StringType, Optional: true,
						Validators: []validator.Set{setvalidator.SizeBetween(1, maxSecretKeysPerTarget), setvalidator.ValueStringsAre(configMapKey{})},
					},
				}},
			},
			"sync_status": schema.ListNestedAttribute{
				Description: "Delivery status of each target, sorted by cluster then namespace. A removed target stays `Withdrawing` until the platform has withdrawn it.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"cluster":     schema.StringAttribute{Description: "Target cluster name.", Computed: true},
					"namespace":   schema.StringAttribute{Description: "Target namespace in the cluster.", Computed: true},
					"secret_name": schema.StringAttribute{Description: "Name of the Kubernetes Secret in the namespace.", Computed: true},
					"phase":       schema.StringAttribute{Description: "`Pending`, `Synced`, `Failed` or `Withdrawing`. `Synced` means the current values have reached the namespace.", Computed: true},
					"reason":      schema.StringAttribute{Description: "Why the target is in its phase, such as `TargetConflict`, `ProviderError` or `AwaitingCopy`.", Computed: true},
				}},
			},
			"phase":      schema.StringAttribute{Description: "`Pending`, `Active`, `Degraded` or `Deleting`. `Active` on its own does not confirm that the current values were delivered; see `sync_status`.", Computed: true},
			"etag":       etagAttribute(),
			"created_at": createdAtAttribute("the managed secret definition"),
			"timeouts":   timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *SecretResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.RequiredTogether(path.MatchRoot("values_wo"), path.MatchRoot("values_wo_version"))}
}

func (r *SecretResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var sync types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("sync"), &sync)...)
	if resp.Diagnostics.HasError() || sync.IsNull() || sync.IsUnknown() {
		return
	}
	count := 0
	for _, elem := range sync.Elements() {
		obj, ok := elem.(types.Object)
		if !ok || obj.IsNull() || obj.IsUnknown() {
			continue
		}
		keys, ok := obj.Attributes()["keys"].(types.Set)
		if ok && !keys.IsNull() && !keys.IsUnknown() {
			// Unknown elements can collapse to a known member of the set
			// later, so only known key names prove the cap is exceeded.
			for _, key := range keys.Elements() {
				if !key.IsNull() && !key.IsUnknown() {
					count++
				}
			}
		}
	}
	if count > maxSecretKeys {
		resp.Diagnostics.AddAttributeError(path.Root("sync"), "Too many selected keys",
			"A secret can select at most 1,024 keys across all its sync targets. Select fewer keys, or split the targets across two secrets.")
	}
}

func (r *SecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultSecretCreateTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !plan.ValuesWOVersion.IsNull() {
		written := r.writeValues(ctx, req.Config, &plan, nil, true, &resp.Diagnostics)
		if written == nil {
			return
		}
		plan.SourceVersion = types.Int64Value(written.Version)
	} else {
		plan.SourceVersion = types.Int64Null()
		r.readSourceVersion(ctx, &plan, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	s, etag, err := r.client.CreateSecret(ctx, client.CreateSecretRequest{
		Name: plan.Name.ValueString(), SecretPath: plan.SecretPath.ValueString(), Sync: extractSyncTargets(plan.Sync),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create secret", apiErrorDetail(err))
		return
	}
	lifecycle := r.managedResource
	if !plan.ValuesWOVersion.IsNull() {
		lifecycle = r.valuesLifecycle(plan.SourceVersion, "")
	}
	lifecycle.created(ctx, s, etag, &plan, &resp.State, &resp.Diagnostics)
}

func (r *SecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, defaultSecretUpdateTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lifecycle := r.managedResource
	if !plan.ValuesWOVersion.IsNull() && !plan.ValuesWOVersion.Equal(state.ValuesWOVersion) {
		// A refused or uncertain write keeps the last confirmed counter.
		resp.State = req.State
		written := r.writeValues(ctx, req.Config, &plan, &state, false, &resp.Diagnostics)
		if written == nil {
			return
		}
		plan.SourceVersion = types.Int64Value(written.Version)
		confirmed := state
		confirmed.ValuesWOVersion = plan.ValuesWOVersion
		confirmed.SourceVersion = plan.SourceVersion
		resp.Diagnostics.Append(resp.State.Set(ctx, &confirmed)...)
		if resp.Diagnostics.HasError() {
			return
		}
		token, err := r.deliveryToken(ctx, plan.Name.ValueString(), written)
		if err != nil {
			resp.Diagnostics.AddError("values saved; delivery not confirmed", savedValuesDetail(written.Version))
			return
		}
		lifecycle = r.valuesLifecycle(plan.SourceVersion, token)
	} else {
		// Removing the inputs changes only the local counter; stored values
		// and their last observed version are retained.
		plan.SourceVersion = state.SourceVersion
	}
	var send patchFunc[client.Secret]
	if !plan.Sync.Equal(state.Sync) {
		send = func(ctx context.Context, etag string) (*client.Secret, string, error) {
			return r.client.UpdateSecret(ctx, plan.Name.ValueString(), etag, client.PatchSecretRequest{Sync: extractSyncTargets(plan.Sync)})
		}
	}
	lifecycle.update(ctx, &plan, &state, state.ETag.ValueString(), send, true, &resp.State, &resp.Diagnostics)
}

func extractSyncTargets(list types.List) []client.SyncTarget {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	// Preserve [] so clearing the complete desired target list reaches the API.
	targets := make([]client.SyncTarget, 0, len(list.Elements()))
	for _, elem := range list.Elements() {
		obj, ok := elem.(types.Object)
		if !ok {
			continue
		}
		attrs := obj.Attributes()
		cluster, _ := attrs["cluster"].(types.String)
		namespace, _ := attrs["namespace"].(types.String)
		secretName, _ := attrs["secret_name"].(types.String)
		keys, _ := attrs["keys"].(types.Set)
		targets = append(targets, client.SyncTarget{Cluster: cluster.ValueString(), Namespace: namespace.ValueString(),
			SecretName: secretName.ValueString(), Keys: setStrings(keys)})
	}
	return targets
}

// secretTargetsEqual compares the complete mutable spec. Keys are a set;
// the target list keeps its order, matching the sync schema.
func secretTargetsEqual(a, b []client.SyncTarget) bool {
	return slices.EqualFunc(a, b, func(a, b client.SyncTarget) bool {
		return a.Cluster == b.Cluster && a.Namespace == b.Namespace && a.SecretName == b.SecretName &&
			slices.Equal(sortedCopy(a.Keys), sortedCopy(b.Keys))
	})
}

func mapSecretToState(s *client.Secret, etag string, state *SecretResourceModel) {
	// The apply configuration is the only place values may be read.
	state.ValuesWO = types.MapNull(types.StringType)
	state.Name = types.StringValue(s.Name)
	state.SecretPath = types.StringValue(s.SecretPath)
	state.ETag = types.StringValue(etag)
	state.CreatedAt = types.StringValue(s.CreatedAt)
	state.Phase = types.StringValue(secretPhase(s))
	state.SyncStatus = secretSyncStatus(s.Status)
	// API [] cannot distinguish an omitted sync from an explicitly empty
	// list; retain the planned/prior null for existing configurations.
	if len(s.Sync) == 0 && state.Sync.IsNull() {
		state.Sync = types.ListNull(types.ObjectType{AttrTypes: syncTargetAttrTypes})
		return
	}
	elems := make([]attr.Value, 0, len(s.Sync))
	for _, target := range s.Sync {
		name := types.StringNull()
		if target.SecretName != "" {
			name = types.StringValue(target.SecretName)
		}
		keys := types.SetNull(types.StringType)
		if len(target.Keys) > 0 {
			keys = stringSet(target.Keys)
		}
		elems = append(elems, types.ObjectValueMust(syncTargetAttrTypes, map[string]attr.Value{
			"cluster": types.StringValue(target.Cluster), "namespace": types.StringValue(target.Namespace),
			"secret_name": name, "keys": keys,
		}))
	}
	state.Sync = types.ListValueMust(types.ObjectType{AttrTypes: syncTargetAttrTypes}, elems)
}

func secretSyncStatus(status *client.SecretStatus) types.List {
	var sorted []client.SecretTargetStatus
	if status != nil {
		sorted = slices.Clone(status.SyncStatuses)
	}
	slices.SortFunc(sorted, func(a, b client.SecretTargetStatus) int {
		if c := strings.Compare(a.Cluster, b.Cluster); c != 0 {
			return c
		}
		if c := strings.Compare(a.Namespace, b.Namespace); c != 0 {
			return c
		}
		if c := strings.Compare(a.SecretName, b.SecretName); c != 0 {
			return c
		}
		if c := strings.Compare(a.Phase, b.Phase); c != 0 {
			return c
		}
		return strings.Compare(a.Reason, b.Reason)
	})
	elems := make([]attr.Value, 0, len(sorted))
	for _, target := range sorted {
		elems = append(elems, types.ObjectValueMust(secretTargetStatusType.AttrTypes, map[string]attr.Value{
			"cluster": types.StringValue(target.Cluster), "namespace": types.StringValue(target.Namespace),
			"secret_name": types.StringValue(target.SecretName), "phase": types.StringValue(target.Phase), "reason": types.StringValue(target.Reason),
		}))
	}
	return types.ListValueMust(secretTargetStatusType, elems)
}
