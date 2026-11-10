package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Binding phases Create and Update stop on. Pending is in progress: a
// binding to a cluster that does not exist yet becomes Ready once the
// cluster does. Failed is terminal for the wait: waiting does not clear it,
// the tenant has to act.
const (
	databaseBindingPhaseReady  = "Ready"
	databaseBindingPhaseFailed = "Failed"
)

// databaseBindingConditionConflict is the condition that is True while an
// object the platform did not create holds the binding's Service, Endpoints
// or Secret name in a cluster namespace.
const databaseBindingConditionConflict = "Conflict"

// Default timeouts for binding operations, overridable in the `timeouts`
// block. Delivering a Service and Secret into a cluster takes seconds.
const (
	defaultDatabaseBindingCreateTimeout = 5 * time.Minute
	defaultDatabaseBindingUpdateTimeout = 10 * time.Minute
	defaultDatabaseBindingDeleteTimeout = 5 * time.Minute
)

// maxNamespacesPerBinding mirrors the binding CRD's maxItems on
// spec.namespaces (the operator's MaxNamespacesPerBinding). The cap of 20
// namespaces across all bindings of one database needs the database's other
// bindings, so it is left to the platform.
const maxNamespacesPerBinding = 10

var (
	_ resource.Resource                = &DatabaseBindingResource{}
	_ resource.ResourceWithConfigure   = &DatabaseBindingResource{}
	_ resource.ResourceWithImportState = &DatabaseBindingResource{}
)

// DatabaseBindingResource is kupe_database_binding. Read, Delete and
// Configure come from managedResource; it imports by "<database>/<binding>".
type DatabaseBindingResource struct {
	managedResource[DatabaseBindingResourceModel, client.DatabaseBinding]
}

type DatabaseBindingResourceModel struct {
	LogicalDatabase  types.String   `tfsdk:"logical_database"`
	RoleName         types.String   `tfsdk:"role_name"`
	Credentials      types.Object   `tfsdk:"credentials"`
	ObservedRotation types.String   `tfsdk:"observed_rotation"`
	Name             types.String   `tfsdk:"name"`
	Database         types.String   `tfsdk:"database"`
	Cluster          types.String   `tfsdk:"cluster"`
	Namespaces       types.Set      `tfsdk:"namespaces"`
	Role             types.String   `tfsdk:"role"`
	SecretName       types.String   `tfsdk:"secret_name"`
	Phase            types.String   `tfsdk:"phase"`
	NamespaceStatus  types.List     `tfsdk:"namespace_status"`
	ETag             types.String   `tfsdk:"etag"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

// databaseBindingNamespaceStatusType is one element of namespace_status.
var databaseBindingNamespaceStatusType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"delivered_rotation": types.StringType,
	"name":               types.StringType,
	"host":               types.StringType,
	"phase":              types.StringType,
	"message":            types.StringType,
}}

func NewDatabaseBindingResource() resource.Resource {
	return &DatabaseBindingResource{
		managedResource[DatabaseBindingResourceModel, client.DatabaseBinding]{kind: &databaseBindingKind},
	}
}

// databaseBindingKind is the binding's lifecycle for managedResource. Only
// namespaces and dedicated credentials can be updated; a 412 is retried
// once only when all mutable settings are unchanged.
var databaseBindingKind = serviceKind[DatabaseBindingResourceModel, client.DatabaseBinding]{
	noun: "database binding",
	name: func(m *DatabaseBindingResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *DatabaseBindingResourceModel) (*client.DatabaseBinding, string, error) {
		return c.GetDatabaseBinding(ctx, m.Database.ValueString(), m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *DatabaseBindingResourceModel) error {
		return c.DeleteDatabaseBinding(ctx, m.Database.ValueString(), m.Name.ValueString())
	},
	mapToState: mapDatabaseBindingToState,
	timeoutsOf: func(m *DatabaseBindingResourceModel) timeouts.Value { return m.Timeouts },
	defaults: operationTimeouts{
		create: defaultDatabaseBindingCreateTimeout,
		update: defaultDatabaseBindingUpdateTimeout,
		delete: defaultDatabaseBindingDeleteTimeout,
	},
	ready: readiness[DatabaseBindingResourceModel, client.DatabaseBinding]{
		target:    databaseBindingPhaseReady,
		phase:     databaseBindingPhase,
		converged: databaseBindingDelivered,
		// Waiting does not clear Failed: the tenant has to act.
		failed: func(b *client.DatabaseBinding) (time.Duration, bool) {
			return 0, databaseBindingFailed(b)
		},
		// On create, Terraform taints a resource whose Create returns an
		// error along with a state, so the failed binding stays tracked:
		// `terraform destroy` removes it and the next apply replaces it.
		failedDetail: databaseBindingFailedDetail,
		timedOutDetail: func(m *DatabaseBindingResourceModel, b *client.DatabaseBinding) string {
			detail := fmt.Sprintf(" If the cluster %q does not exist yet, the binding becomes Ready once it does.",
				m.Cluster.ValueString())
			if b != nil && b.Status != nil && b.Status.Message != "" {
				detail += "\n\nStatus message: " + b.Status.Message
			}
			return detail
		},
	},
	settingsChanged: func(prior *DatabaseBindingResourceModel, current *client.DatabaseBinding) []string {
		var changed []string
		if !slices.Equal(setStrings(prior.Namespaces), sortedCopy(current.Namespaces)) {
			changed = append(changed, "namespaces")
		}
		if bindingDedicated(prior.Credentials) != current.Credentials.Dedicated {
			changed = append(changed, "credentials.dedicated")
		}
		return changed
	},
	stillDeleting: func(m *DatabaseBindingResourceModel) string {
		return fmt.Sprintf("DELETE was accepted but binding %q is still being removed after the configured delete "+
			"timeout. Terraform has removed it from state; the Kupe platform finishes removing the Service, "+
			"Secret and network access in the background.", m.Name.ValueString())
	},
}

// databaseBindingPhase is b's phase, "" when it has no status yet.
func databaseBindingPhase(b *client.DatabaseBinding) string {
	if b == nil || b.Status == nil {
		return ""
	}
	return b.Status.Phase
}

// databaseBindingDelivered reports whether the platform has delivered the
// binding's current namespace list: the binding is Ready, every listed
// namespace is Ready, and no namespace taken out of the list is still being
// withdrawn. The delivered generation must also catch up: right after a
// PATCH the status can still describe the previous credentials.
func databaseBindingDelivered(b *client.DatabaseBinding) bool {
	if databaseBindingPhase(b) != databaseBindingPhaseReady || b.Status.ObservedGeneration < b.Generation || databaseBindingConflicted(b.Status) || len(b.Status.Namespaces) != len(b.Namespaces) {
		return false
	}
	for _, ns := range b.Status.Namespaces {
		if ns.Phase != databaseBindingPhaseReady || !slices.Contains(b.Namespaces, ns.Name) {
			return false
		}
	}
	return true
}

// databaseBindingFailedNamespaces are the namespaces of b's list whose
// status is Failed, sorted. A namespace taken out of the list is not one of
// them: it is being withdrawn, and its old status says nothing about the new
// list.
func databaseBindingFailedNamespaces(b *client.DatabaseBinding) []string {
	if b == nil || b.Status == nil {
		return nil
	}
	var failed []string
	for _, ns := range b.Status.Namespaces {
		if ns.Phase == databaseBindingPhaseFailed && slices.Contains(b.Namespaces, ns.Name) {
			failed = append(failed, ns.Name)
		}
	}
	slices.Sort(failed)
	return failed
}

func (r *DatabaseBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_binding"
}

func (r *DatabaseBindingResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Every argument but `namespaces` replaces the binding when it changes.
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Binds a Kupe database into namespaces of one cluster for one role. The Kupe platform " +
			"delivers a Service named after the database and a credentials Secret into each listed namespace and " +
			"opens the network path; other namespaces see neither. A binding lists 1 to 10 namespaces, and the " +
			"bindings of one database list at most 20 between them. Create and Update wait for the current generation and every listed " +
			"namespace to be Ready, and fail at once if one is Failed: it already has a Service or Endpoints object " +
			"named after the database, or a Secret named `secret_name`, that the platform did not create. A " +
			"binding that fails or times out on create stays in state, marked tainted. Delete waits until the " +
			"binding is gone. " +
			"Set timeouts with the `timeouts` attribute, for example `timeouts = { create = \"10m\" }` (defaults: " +
			"create/delete 5m, update 10m).\n\n" +
			"`namespaces` changes in place: a namespace added gets the Service and the Secret, and the namespaces " +
			"kept work throughout. Enabling dedicated credentials changes in place; turning them off replaces the binding. Changing `credentials.rotation_trigger` requests a dedicated rotation. Other arguments except `timeouts` replace the binding. Removing a " +
			"namespace, or the binding, removes the Secret there, and the Service and the network path too unless " +
			"another binding of the same database still lists that namespace. With shared credentials, the password does not change: " +
			"rotate the role's credentials if the namespace should lose knowledge of it.",
		Attributes: map[string]schema.Attribute{
			"logical_database":  schema.StringAttribute{Optional: true, Computed: true, Description: "PostgreSQL database to bind. Omit it to use the first database, including a restored instance's preserved first name. Changing it replaces the binding. Added databases include their logical name in default Secret names. See `name` for the binding resource name. If a default name is taken, set `name` or `secret_name` explicitly.", Validators: logicalNameValidators(53), PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}},
			"role_name":         schema.StringAttribute{Optional: true, Description: "Declared role granted by a custom binding. Required for custom and refused for built-in roles. Changing it replaces the binding.", Validators: []validator.String{stringvalidator.LengthBetween(1, 63), stringvalidator.RegexMatches(bindingRoleNamePattern, "must start with a lowercase letter and contain only lowercase letters, numbers or _")}, PlanModifiers: replace},
			"credentials":       bindingCredentialsSchema(),
			"observed_rotation": schema.StringAttribute{Computed: true, Description: "Dedicated rotation token applied and delivered to every listed namespace; empty for shared credentials."},
			"name": schema.StringAttribute{
				Description: "Binding name. Defaults to `{database}-{cluster}-{role}` when `logical_database` is omitted or equals the instance name with hyphens replaced by underscores. Other explicit selections use `{database}-{logical_database}-{cluster}-{role}`, including a restored first database whose name differs from the normalised instance name. Uses `role_name` for custom and replaces underscores with hyphens. If `logical_database` is explicitly unknown during planning, the default name becomes known when the selection is known. Set `name` explicitly if the default is taken, " +
					"longer than 63 characters, or you need a second binding of the same role " +
					"into the same cluster.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					databaseBindingNameDefault(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"database": schema.StringAttribute{
				Description:   "Name of the database to bind.",
				Required:      true,
				Validators:    databaseRefValidators(),
				PlanModifiers: replace,
			},
			"cluster": schema.StringAttribute{
				Description: "Cluster to bind into. It may not exist yet: the binding becomes Ready once it does, " +
					"so Create waits up to its timeout and then fails.",
				Required:      true,
				Validators:    clusterRefValidators(),
				PlanModifiers: replace,
			},
			"namespaces": schema.SetAttribute{
				Description: "Namespaces inside the cluster that receive the Service and credentials Secret: 1 to " +
					"10, none `kube-*` or `argocd`. Changes in place.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeBetween(1, maxNamespacesPerBinding),
					setvalidator.ValueStringsAre(bindingNamespaceValidators()...),
				},
			},
			"role": schema.StringAttribute{
				Description:   "Role granted: `owner` (can change the schema), `readwrite` (reads and writes data), `readonly` (reads data), or `custom` (a declared role).",
				Required:      true,
				PlanModifiers: replace,
				Validators: []validator.String{
					stringvalidator.OneOf("owner", "readwrite", "readonly", "custom"),
				},
			},
			"secret_name": schema.StringAttribute{
				Description: "Name of the credentials Secret in every listed namespace. When unset, a new binding uses " +
					"`{database}-{role}` for the first PostgreSQL database or `{database}-{logical_database}-{role}` for an added database (using `role_name` for custom, with underscores replaced by hyphens). A taken default is refused; set `secret_name` explicitly. An existing or imported binding keeps its current name, so removing the " +
					"attribute from configuration does not change the name back: set it to `{database}-{role}` " +
					"instead. A different name replaces the binding. The Secret holds `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, " +
					"`ca.crt` and `uri`; mount it at `/bindings/<secret_name>`, where `uri` expects `ca.crt`.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending (waiting for the database, the cluster or a namespace), Ready " +
					"(every listed namespace is Ready), or Failed (a listed namespace already has a Service, Endpoints " +
					"object or Secret of the same name that the platform did not create).",
				Computed: true,
			},
			"namespace_status": schema.ListNestedAttribute{
				Description: "What the binding delivered into each namespace, sorted by name. A namespace removed " +
					"from `namespaces` stays listed until its Service and Secret are withdrawn.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"delivered_rotation": schema.StringAttribute{Computed: true, Description: "Rotation token carried by the credentials Secret delivered for this namespace."},
						"name": schema.StringAttribute{
							Description: "Namespace inside the cluster.",
							Computed:    true,
						},
						"host": schema.StringAttribute{
							Description: "In-cluster DNS name applications in this namespace connect to. For a " +
								"`readwrite`, `readonly` or custom binding of a database whose connection pooler is on and " +
								"serving, it is the pooler's, e.g. `orders-db--pooler.orders-api.svc.cluster.local`; " +
								"otherwise, and always for an `owner` binding, it is the database's, e.g. " +
								"`orders-db.orders-api.svc.cluster.local`. Empty until the Service is delivered.",
							Computed: true,
						},
						"phase": schema.StringAttribute{
							Description: "Pending, Ready, or Failed (the namespace already has a Service, Endpoints " +
								"object or Secret of the same name that the platform did not create).",
							Computed: true,
						},
						"message": schema.StringAttribute{
							Description: "Why the namespace is not Ready; empty when it is.",
							Computed:    true,
						},
					},
				},
			},
			"etag": etagAttribute(),
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

func (r *DatabaseBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DatabaseBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := client.CreateDatabaseBindingRequest{
		LogicalDatabase: plan.LogicalDatabase.ValueString(),
		Name:            plan.Name.ValueString(),
		Cluster:         plan.Cluster.ValueString(),
		Namespaces:      setStrings(plan.Namespaces),
		Role:            plan.Role.ValueString(),
		RoleName:        plan.RoleName.ValueString(),
		Credentials:     &client.BindingCredentials{Dedicated: bindingDedicated(plan.Credentials)},
	}
	if !plan.SecretName.IsUnknown() {
		createReq.SecretName = plan.SecretName.ValueString()
	}
	binding, etag, err := r.client.CreateDatabaseBinding(ctx, plan.Database.ValueString(), createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create database binding", databaseBindingCreateErrorDetail(err))
		return
	}
	r.created(ctx, binding, etag, &plan, &resp.State, &resp.Diagnostics)
}

// Update composes namespace and credential changes in one PATCH, waits for
// delivery, then requests a rotation when its trigger changes.
func (r *DatabaseBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DatabaseBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, d := plan.Timeouts.Update(ctx, defaultDatabaseBindingUpdateTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var patch client.PatchDatabaseBindingRequest
	if namespaces := setStrings(plan.Namespaces); !slices.Equal(namespaces, setStrings(state.Namespaces)) {
		patch.Namespaces = namespaces
	}
	if bindingDedicated(plan.Credentials) != bindingDedicated(state.Credentials) {
		patch.Credentials = &client.BindingCredentials{Dedicated: bindingDedicated(plan.Credentials)}
	}
	var send patchFunc[client.DatabaseBinding]
	if patch.Namespaces != nil || patch.Credentials != nil {
		database, name := plan.Database.ValueString(), plan.Name.ValueString()
		send = func(ctx context.Context, etag string) (*client.DatabaseBinding, string, error) {
			return r.client.UpdateDatabaseBinding(ctx, database, name, etag, patch)
		}
	}
	trigger, priorTrigger := bindingTrigger(plan.Credentials), bindingTrigger(state.Credentials)
	rotate := isSet(trigger) && !trigger.Equal(priorTrigger)
	if rotate {
		plan.Credentials = bindingCredentialsValue(bindingDedicated(plan.Credentials), priorTrigger)
	}
	r.update(ctx, &plan, &state, state.ETag.ValueString(), send, true, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if rotate {
		r.rotateCredentials(ctx, &plan, trigger, resp)
	}
}

// databaseBindingConflicted reports whether the binding's Conflict condition
// is True.
func databaseBindingConflicted(status *client.DatabaseBindingStatus) bool {
	if status == nil {
		return false
	}
	for _, c := range status.Conditions {
		if c.Type == databaseBindingConditionConflict {
			return c.Status == "True"
		}
	}
	return false
}

// databaseBindingFailedDetail explains a binding with a Failed namespace:
// the platform's message, what to change when another object holds the
// binding's names, and what Terraform does next after op ("create" or
// "update").
func databaseBindingFailedDetail(op string, m *DatabaseBindingResourceModel, b *client.DatabaseBinding) string {
	detail := fmt.Sprintf("Binding %q of database %q failed.", m.Name.ValueString(), m.Database.ValueString())
	var status *client.DatabaseBindingStatus
	if b != nil {
		status = b.Status
	}
	if status != nil && status.Message != "" {
		detail += "\n\nStatus message: " + status.Message
	}
	if failed := databaseBindingFailedNamespaces(b); len(failed) > 0 && databaseBindingConflicted(status) {
		if len(failed) == 1 {
			detail += fmt.Sprintf("\n\nNamespace %s of cluster %q already has a Service or Endpoints object named "+
				"%q, or a Secret named %q, that the Kupe platform did not create, and the binding does not replace "+
				"it. Rename or remove that object, or, if the Secret is the clash, set a different `secret_name`.",
				quotedList(failed), m.Cluster.ValueString(), m.Database.ValueString(), m.SecretName.ValueString())
		} else {
			detail += fmt.Sprintf("\n\nNamespaces %s of cluster %q each already have a Service or Endpoints object "+
				"named %q, or a Secret named %q, that the Kupe platform did not create, and the binding does not "+
				"replace them. Rename or remove those objects, or, if a Secret is the clash, set a different "+
				"`secret_name`.",
				quotedList(failed), m.Cluster.ValueString(), m.Database.ValueString(), m.SecretName.ValueString())
		}
	}
	if op == "update" {
		return detail + "\n\nThe other namespaces keep working. " + changeRecordedNotice
	}
	return detail + "\n\nTerraform keeps the binding in state, marked tainted: the next `terraform apply` " +
		"replaces it and `terraform destroy` removes it."
}

// quotedList is names quoted and joined for a sentence: "a", "a" and "b",
// "a", "b" and "c".
func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	if len(quoted) < 2 {
		return strings.Join(quoted, "")
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// ImportState accepts "<database>/<binding>".
func (r *DatabaseBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	database, name, ok := parseDatabaseBindingImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("invalid import ID",
			fmt.Sprintf("expected \"<database>/<binding>\", got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database"), database)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

func parseDatabaseBindingImportID(id string) (database, name string, ok bool) {
	database, name, found := strings.Cut(id, "/")
	if !found || database == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return database, name, true
}

func mapDatabaseBindingToState(b *client.DatabaseBinding, etag string, state *DatabaseBindingResourceModel) {
	state.Name = types.StringValue(b.Name)
	state.Database = types.StringValue(b.Database)
	state.Cluster = types.StringValue(b.Cluster)
	state.Namespaces = stringSet(b.Namespaces)
	state.Role = types.StringValue(b.Role)
	state.LogicalDatabase = stringOrNull(b.LogicalDatabase)
	state.RoleName = stringOrNull(b.RoleName)
	state.Credentials = bindingCredentialsValue(b.Credentials.Dedicated, bindingTrigger(state.Credentials))
	state.ObservedRotation = types.StringValue("")
	if b.Status != nil {
		state.ObservedRotation = types.StringValue(b.Status.ObservedRotation)
	}
	state.ETag = types.StringValue(etag)

	phase, secretName := "", b.SecretName
	var namespaces []client.DatabaseBindingNamespaceStatus
	if b.Status != nil {
		phase, namespaces = b.Status.Phase, b.Status.Namespaces
		// The spec carries the name only when one was requested; the
		// status carries the effective name once the platform has seen it.
		if secretName == "" {
			secretName = b.Status.SecretName
		}
	}
	if secretName == "" {
		// Neither says yet (the platform has not reconciled the binding):
		// the effective name is the platform's default. Leaving it null
		// would let a later plan see it unknown and, through
		// RequiresReplace, replace the binding.
		role := b.Role
		if role == "custom" {
			role = strings.ReplaceAll(b.RoleName, "_", "-")
		}
		parts := []string{b.Database}
		if b.LogicalDatabase != "" && b.LogicalDatabase != b.FirstLogicalDatabase {
			parts = append(parts, strings.ReplaceAll(b.LogicalDatabase, "_", "-"))
		}
		parts = append(parts, role)
		secretName = strings.Join(parts, "-")
	}
	state.Phase = types.StringValue(phase)
	state.SecretName = types.StringValue(secretName)
	state.NamespaceStatus = databaseBindingNamespaceStatusList(namespaces)
}

// databaseBindingNamespaceStatusList is namespace_status: one element per
// namespace in the status, sorted by name, an empty list when there is none.
func databaseBindingNamespaceStatusList(namespaces []client.DatabaseBindingNamespaceStatus) types.List {
	sorted := slices.Clone(namespaces)
	slices.SortFunc(sorted, func(a, b client.DatabaseBindingNamespaceStatus) int { return strings.Compare(a.Name, b.Name) })
	elems := make([]attr.Value, 0, len(sorted))
	for _, ns := range sorted {
		elems = append(elems, types.ObjectValueMust(databaseBindingNamespaceStatusType.AttrTypes, map[string]attr.Value{
			"delivered_rotation": types.StringValue(ns.DeliveredRotation),
			"name":               types.StringValue(ns.Name),
			"host":               types.StringValue(ns.Host),
			"phase":              types.StringValue(ns.Phase),
			"message":            types.StringValue(ns.Message),
		}))
	}
	return types.ListValueMust(databaseBindingNamespaceStatusType, elems)
}

// stringSet is a set of strings, empty for nil.
func stringSet(values []string) types.Set {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elems)
}

// setStrings is the known string elements of a set, sorted, so the API gets
// the same list for the same set. It is nil for a null or unknown set.
func setStrings(set types.Set) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	values := make([]string, 0, len(set.Elements()))
	for _, elem := range set.Elements() {
		if s, ok := elem.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			values = append(values, s.ValueString())
		}
	}
	slices.Sort(values)
	return values
}

// sortedCopy is a sorted copy of values.
func sortedCopy(values []string) []string {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted
}

func databaseBindingFailed(b *client.DatabaseBinding) bool {
	if b == nil || b.Status == nil {
		return false
	}
	if len(databaseBindingFailedNamespaces(b)) > 0 {
		return true
	}
	if b.Status.Phase != databaseBindingPhaseFailed && !databaseBindingConflicted(b.Status) {
		return false
	}
	// Generation advances only on successful delivery, so it cannot gate errors.
	// Ignore aggregate errors that still describe namespaces being withdrawn.
	for _, ns := range b.Status.Namespaces {
		if !slices.Contains(b.Namespaces, ns.Name) {
			return false
		}
	}
	return true
}

func databaseBindingCreateErrorDetail(err error) string {
	detail := apiErrorDetail(err)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusConflict && strings.Contains(apiErr.Message, "already exists") {
			return detail + "\n\nSet `name` explicitly to another binding name."
		}
		if apiErr.StatusCode == http.StatusBadRequest && strings.Contains(apiErr.Message, "secret name") && strings.Contains(apiErr.Message, "already used") {
			return detail + "\n\nSet `secret_name` explicitly to another Secret name."
		}
	}
	return detail
}
