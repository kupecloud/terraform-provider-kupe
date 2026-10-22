package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Cluster connection phases Create and Update stop on. Pending is in
// progress; a Failed push namespace ends the wait: waiting does not clear it.
const (
	registryBindingPhaseReady  = "Ready"
	registryBindingPhaseFailed = "Failed"
)

// The push Secret names of a cluster connection (kupe-control-operator
// domain.DefaultRegistryPushSecretName and RegistryPullSecretName). The pull
// name is the platform's own and can never be a push Secret's.
const (
	defaultRegistryPushSecretName = "kupe-registry-push" // #nosec G101 -- a Secret name, not a credential
	registryPullSecretName        = "kupe-registry-pull" // #nosec G101 -- a Secret name, not a credential
)

// registryBindingRestartNotice is the warning Create gives once a cluster
// connection is made (review M-1): the pull credential reaches a pod when it
// starts.
const registryBindingRestartNotice = "Pods that were running before this connection was made keep their old " +
	"settings: restart them to pull from the registry."

// Default timeouts for cluster connection operations, overridable in the
// `timeouts` block. Making the credentials and delivering push Secrets takes
// seconds.
const (
	defaultRegistryBindingCreateTimeout = 5 * time.Minute
	defaultRegistryBindingUpdateTimeout = 5 * time.Minute
	defaultRegistryBindingDeleteTimeout = 5 * time.Minute
)

// secretNamePattern is a DNS-1123 subdomain, the Kubernetes Secret name rule.
var secretNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// maxSecretNameLength is the Kubernetes limit on a DNS-1123 subdomain.
const maxSecretNameLength = 253

var (
	_ resource.Resource                = &RegistryBindingResource{}
	_ resource.ResourceWithConfigure   = &RegistryBindingResource{}
	_ resource.ResourceWithImportState = &RegistryBindingResource{}
)

// RegistryBindingResource is kupe_registry_binding, a cluster connection.
// Read, Delete and Configure come from managedResource; it imports by
// "registry/<cluster>".
type RegistryBindingResource struct {
	managedResource[RegistryBindingResourceModel, client.RegistryBinding]
}

type RegistryBindingResourceModel struct {
	Registry        types.String   `tfsdk:"registry"`
	Cluster         types.String   `tfsdk:"cluster"`
	Name            types.String   `tfsdk:"name"`
	PushNamespaces  types.Set      `tfsdk:"push_namespaces"`
	SecretName      types.String   `tfsdk:"secret_name"`
	Phase           types.String   `tfsdk:"phase"`
	Message         types.String   `tfsdk:"message"`
	PullUsername    types.String   `tfsdk:"pull_username"`
	PushUsername    types.String   `tfsdk:"push_username"`
	NamespaceStatus types.List     `tfsdk:"namespace_status"`
	CreatedAt       types.String   `tfsdk:"created_at"`
	ETag            types.String   `tfsdk:"etag"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

// registryBindingNamespaceStatusType is one element of namespace_status
// (standards bindings.md rule 25: {name, phase, message}).
var registryBindingNamespaceStatusType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name":    types.StringType,
	"phase":   types.StringType,
	"message": types.StringType,
}}

func NewRegistryBindingResource() resource.Resource {
	return &RegistryBindingResource{
		managedResource[RegistryBindingResourceModel, client.RegistryBinding]{kind: &registryBindingKind},
	}
}

// registryBindingKind is the cluster connection's lifecycle for
// managedResource. Only `push_namespaces` can be updated; a 412 is retried
// once when it is unchanged.
var registryBindingKind = serviceKind[RegistryBindingResourceModel, client.RegistryBinding]{
	noun: "cluster connection",
	name: func(m *RegistryBindingResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *RegistryBindingResourceModel) (*client.RegistryBinding, string, error) {
		return c.GetRegistryBinding(ctx, m.Registry.ValueString(), m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *RegistryBindingResourceModel) error {
		return c.DeleteRegistryBinding(ctx, m.Registry.ValueString(), m.Name.ValueString())
	},
	mapToState: mapRegistryBindingToState,
	timeoutsOf: func(m *RegistryBindingResourceModel) timeouts.Value { return m.Timeouts },
	defaults: operationTimeouts{
		create: defaultRegistryBindingCreateTimeout,
		update: defaultRegistryBindingUpdateTimeout,
		delete: defaultRegistryBindingDeleteTimeout,
	},
	ready: readiness[RegistryBindingResourceModel, client.RegistryBinding]{
		target:    registryBindingPhaseReady,
		phase:     registryBindingPhase,
		converged: registryBindingDelivered,
		failed: func(b *client.RegistryBinding) (time.Duration, bool) {
			return 0, len(registryBindingFailedNamespaces(b)) > 0
		},
		failedDetail: registryBindingFailedDetail,
		timedOutDetail: func(_ *RegistryBindingResourceModel, b *client.RegistryBinding) string {
			if b != nil && b.Status != nil && b.Status.Message != "" {
				return "\n\nStatus message: " + b.Status.Message
			}
			return ""
		},
	},
	settingsChanged: func(prior *RegistryBindingResourceModel, current *client.RegistryBinding) []string {
		if slices.Equal(setStrings(prior.PushNamespaces), sortedCopy(current.PushNamespaces)) {
			return nil
		}
		return []string{"push_namespaces"}
	},
	stillDeleting: func(m *RegistryBindingResourceModel) string {
		return fmt.Sprintf("DELETE was accepted but cluster connection %q is still being removed after the "+
			"configured delete timeout. Terraform has removed it from state; the Kupe platform finishes deleting its "+
			"credentials and push Secrets in the background.", m.Name.ValueString())
	},
}

// registryBindingPhase is b's phase, "" when it has no status yet.
func registryBindingPhase(b *client.RegistryBinding) string {
	if b == nil || b.Status == nil {
		return ""
	}
	return b.Status.Phase
}

// registryBindingDelivered reports whether the connection is Ready with
// every listed push namespace Ready. A connection has no generation, so
// right after a PATCH its status may still describe the previous list: a
// namespace added is not Ready there yet. A namespace taken out may stay in
// the status while its Secret is withdrawn; it is not waited for, because
// the platform holds the withdrawal while registries are switched off or the
// tenant is suspended.
func registryBindingDelivered(b *client.RegistryBinding) bool {
	if registryBindingPhase(b) != registryBindingPhaseReady {
		return false
	}
	for _, ns := range b.PushNamespaces {
		if !slices.ContainsFunc(b.Status.Namespaces, func(s client.RegistryBindingNamespaceStatus) bool {
			return s.Name == ns && s.Phase == registryBindingPhaseReady
		}) {
			return false
		}
	}
	return true
}

// registryBindingFailedNamespaces are the listed push namespaces whose
// status is Failed, sorted. A namespace taken out of the list is not one of
// them: its old status says nothing about the new list.
func registryBindingFailedNamespaces(b *client.RegistryBinding) []string {
	if b == nil || b.Status == nil {
		return nil
	}
	var failed []string
	for _, ns := range b.Status.Namespaces {
		if ns.Phase == registryBindingPhaseFailed && slices.Contains(b.PushNamespaces, ns.Name) {
			failed = append(failed, ns.Name)
		}
	}
	slices.Sort(failed)
	return failed
}

// registryBindingFailedDetail explains a connection with a Failed push
// namespace and what Terraform does next after op ("create" or "update").
func registryBindingFailedDetail(op string, m *RegistryBindingResourceModel, b *client.RegistryBinding) string {
	detail := fmt.Sprintf("Cluster connection %q could not deliver the push credential to namespace %s.",
		m.Name.ValueString(), quotedList(registryBindingFailedNamespaces(b)))
	if b != nil && b.Status != nil && b.Status.Message != "" {
		detail += "\n\nStatus message: " + b.Status.Message
	}
	detail += fmt.Sprintf("\n\nUsually another Secret named %q already exists there that the Kupe platform did not "+
		"create, and the connection does not replace it. Rename or remove that Secret, or set a different "+
		"`secret_name`. Every pod of the cluster can still pull.", m.SecretName.ValueString())
	if op == "update" {
		return detail + "\n\n" + changeRecordedNotice
	}
	return detail + "\n\nTerraform keeps the cluster connection in state, marked tainted: the next `terraform " +
		"apply` replaces it and `terraform destroy` removes it."
}

func (r *RegistryBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_binding"
}

func (r *RegistryBindingResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Connects a cluster to the tenant's Kupe container registry. Every pod of the cluster can " +
			"then pull its images: the pull credential stays on the platform and never enters the cluster. Each " +
			"namespace in `push_namespaces` also gets a push credential Secret. A cluster has one connection, named " +
			"after it, and the cluster must exist. Create and Update wait until the connection is Ready with every " +
			"push namespace delivered, and fail at once if a push namespace is Failed. A connection that fails or " +
			"times out on create stays in state, marked tainted. Delete waits until the connection is gone. Set " +
			"timeouts with the `timeouts` attribute, for example `timeouts = { create = \"10m\" }` (defaults: " +
			"create/update/delete 5m).\n\n" +
			"Pods that were running before the connection was made keep their old settings: restart them to pull " +
			"from the registry. `push_namespaces` changes in place. Changing `cluster`, `registry` or " +
			"`secret_name` replaces the connection: it is deleted and created again, so its pull and push " +
			"credentials are reissued. Credentials are never returned by the API or stored in Terraform state.",
		Attributes: map[string]schema.Attribute{
			"registry": schema.StringAttribute{
				Description: "Registry to connect to. A tenant has one registry, named `registry`: this is the " +
					"default and the only value accepted.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString(registryName),
				Validators: registryNameValidators(),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cluster": schema.StringAttribute{
				Description:   "Cluster to connect. It must exist. A different cluster replaces the connection.",
				Required:      true,
				Validators:    clusterRefValidators(),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Description: "Connection name: always the cluster's name.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"push_namespaces": schema.SetAttribute{
				Description: "Namespaces inside the cluster that receive a push credential Secret named " +
					"`secret_name`: up to 10, none `kube-*` or `argocd`. Each namespace receives a Secret named " +
					"`secret_name` that can pull from and push to every repository in your registry, including " +
					"replacing existing tags. Default none: the cluster can pull only. Changes in place: a namespace " +
					"added gets the Secret, one taken out loses it, and pulls keep working throughout. The " +
					"registry's connections can list at most 20 push namespaces between them; the Kupe platform " +
					"checks that when the change is applied.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})),
				Validators: []validator.Set{
					setvalidator.SizeAtMost(maxNamespacesPerBinding),
					setvalidator.ValueStringsAre(bindingNamespaceValidators()...),
				},
			},
			"secret_name": schema.StringAttribute{
				Description: "Name of the push credential Secret in each push namespace. Default " +
					"`kupe-registry-push`; it cannot be `kupe-registry-pull`. It cannot change in place: a " +
					"different name replaces the cluster connection (it is deleted and created again, so its pull " +
					"and push credentials are reissued).",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(defaultRegistryPushSecretName),
				Validators: []validator.String{
					stringvalidator.LengthAtMost(maxSecretNameLength),
					stringvalidator.RegexMatches(secretNamePattern,
						"must be lowercase letters, digits, '-' and '.', and start and end with a letter or digit"),
					stringvalidator.NoneOfCaseInsensitive(registryPullSecretName),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending, Ready (every pod can pull and every push namespace is " +
					"delivered) or Failed (a push namespace could not be delivered).",
				Computed: true,
			},
			"message": schema.StringAttribute{
				Description: "What the platform says about the connection.",
				Computed:    true,
			},
			"pull_username": schema.StringAttribute{
				Description: "Registry username of the cluster's pull credential.",
				Computed:    true,
			},
			"push_username": schema.StringAttribute{
				Description: "Registry username of the push credential; empty without push namespaces.",
				Computed:    true,
			},
			"namespace_status": schema.ListNestedAttribute{
				Description: "What the connection delivered into each push namespace, sorted by name. A namespace " +
					"taken out of `push_namespaces` stays listed until its Secret is withdrawn.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "Namespace inside the cluster.",
							Computed:    true,
						},
						"phase": schema.StringAttribute{
							Description: "Pending, Ready or Failed.",
							Computed:    true,
						},
						"message": schema.StringAttribute{
							Description: "Why the namespace is not Ready; empty when it is.",
							Computed:    true,
						},
					},
				},
			},
			"created_at": createdAtAttribute("the cluster connection"),
			"etag":       etagAttribute(),
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

func (r *RegistryBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RegistryBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster := plan.Cluster.ValueString()
	binding, etag, err := r.client.CreateRegistryBinding(ctx, plan.Registry.ValueString(), client.CreateRegistryBindingRequest{
		Cluster:        cluster,
		PushNamespaces: setStrings(plan.PushNamespaces),
		SecretName:     plan.SecretName.ValueString(),
	})
	if err != nil {
		detail := apiErrorDetail(err)
		if client.ErrorCode(err) == client.CodeRegistryBindingExists {
			// The generic hint has a placeholder; here the cluster is known.
			detail = strings.Replace(detail, "registry/<cluster>", registryName+"/"+cluster, 1)
		}
		resp.Diagnostics.AddError("failed to create cluster connection", detail)
		return
	}
	r.created(ctx, binding, etag, &plan, &resp.State, &resp.Diagnostics)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.AddWarning("restart pods to pull from the registry", registryBindingRestartNotice)
	}
}

// Update sends a changed `push_namespaces` as the complete new list, then
// waits until the platform has delivered it. Every other argument requires
// replacement, so when it is unchanged only `timeouts` changed: the
// connection is refreshed and the new timeouts recorded.
func (r *RegistryBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RegistryBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var send patchFunc[client.RegistryBinding]
	if namespaces := setStrings(plan.PushNamespaces); !slices.Equal(namespaces, setStrings(state.PushNamespaces)) {
		registry, name := plan.Registry.ValueString(), plan.Name.ValueString()
		send = func(ctx context.Context, etag string) (*client.RegistryBinding, string, error) {
			return r.client.UpdateRegistryBinding(ctx, registry, name, etag,
				client.PatchRegistryBindingRequest{PushNamespaces: namespaces})
		}
	}
	r.update(ctx, &plan, &state, state.ETag.ValueString(), send, true, &resp.State, &resp.Diagnostics)
}

// ImportState accepts "registry/<cluster>".
func (r *RegistryBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	registry, name, ok := parseDatabaseBindingImportID(req.ID)
	if !ok || registry != registryName {
		resp.Diagnostics.AddError("invalid import ID",
			fmt.Sprintf("expected \"registry/<cluster>\", got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("registry"), registry)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

func mapRegistryBindingToState(b *client.RegistryBinding, etag string, state *RegistryBindingResourceModel) {
	registry := b.Registry
	if registry == "" {
		registry = registryName
	}
	// The spec carries the name only when one was requested; otherwise the
	// platform's default is in force.
	secretName := b.SecretName
	if secretName == "" {
		secretName = defaultRegistryPushSecretName
	}
	state.Registry = types.StringValue(registry)
	state.Cluster = types.StringValue(b.Cluster)
	state.Name = types.StringValue(b.Name)
	state.PushNamespaces = stringSet(b.PushNamespaces)
	state.SecretName = types.StringValue(secretName)
	state.CreatedAt = types.StringValue(b.CreatedAt)
	state.ETag = types.StringValue(etag)

	var status client.RegistryBindingStatus
	if b.Status != nil {
		status = *b.Status
	}
	state.Phase = types.StringValue(status.Phase)
	state.Message = types.StringValue(status.Message)
	state.PullUsername = types.StringValue(status.PullUsername)
	state.PushUsername = types.StringValue(status.PushUsername)
	state.NamespaceStatus = registryBindingNamespaceStatusList(status.Namespaces)
}

// registryBindingNamespaceStatusList is namespace_status: one element per
// namespace in the status, sorted by name, an empty list when there is none.
func registryBindingNamespaceStatusList(namespaces []client.RegistryBindingNamespaceStatus) types.List {
	sorted := slices.Clone(namespaces)
	slices.SortFunc(sorted, func(a, b client.RegistryBindingNamespaceStatus) int { return strings.Compare(a.Name, b.Name) })
	elems := make([]attr.Value, 0, len(sorted))
	for _, ns := range sorted {
		elems = append(elems, types.ObjectValueMust(registryBindingNamespaceStatusType.AttrTypes, map[string]attr.Value{
			"name":    types.StringValue(ns.Name),
			"phase":   types.StringValue(ns.Phase),
			"message": types.StringValue(ns.Message),
		}))
	}
	return types.ListValueMust(registryBindingNamespaceStatusType, elems)
}
