package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Binding phases Create stops on. Pending is in progress: a binding to a
// cluster that does not exist yet becomes Ready once the cluster does.
// Failed is terminal for Create: waiting does not clear it, the tenant has
// to act.
const (
	databaseBindingPhaseReady  = "Ready"
	databaseBindingPhaseFailed = "Failed"
)

// databaseBindingConditionConflict is the condition that is True while an
// object the platform did not create holds the binding's Service or Secret
// name in the cluster namespace.
const databaseBindingConditionConflict = "Conflict"

// Default timeouts for binding operations, overridable in the `timeouts`
// block. Delivering a Service and Secret into a cluster takes seconds.
const (
	defaultDatabaseBindingCreateTimeout = 5 * time.Minute
	defaultDatabaseBindingDeleteTimeout = 5 * time.Minute
)

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
	Name       types.String   `tfsdk:"name"`
	Database   types.String   `tfsdk:"database"`
	Cluster    types.String   `tfsdk:"cluster"`
	Namespace  types.String   `tfsdk:"namespace"`
	Role       types.String   `tfsdk:"role"`
	SecretName types.String   `tfsdk:"secret_name"`
	Phase      types.String   `tfsdk:"phase"`
	Host       types.String   `tfsdk:"host"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

func NewDatabaseBindingResource() resource.Resource {
	return &DatabaseBindingResource{
		managedResource[DatabaseBindingResourceModel, client.DatabaseBinding]{kind: &databaseBindingKind},
	}
}

// databaseBindingKind is the binding's lifecycle for managedResource. A
// binding has no ETag and cannot be updated, so nothing is retried on a 412.
var databaseBindingKind = serviceKind[DatabaseBindingResourceModel, client.DatabaseBinding]{
	noun: "database binding",
	name: func(m *DatabaseBindingResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *DatabaseBindingResourceModel) (*client.DatabaseBinding, string, error) {
		b, err := c.GetDatabaseBinding(ctx, m.Database.ValueString(), m.Name.ValueString())
		return b, "", err
	},
	del: func(ctx context.Context, c *client.Client, m *DatabaseBindingResourceModel) error {
		return c.DeleteDatabaseBinding(ctx, m.Database.ValueString(), m.Name.ValueString())
	},
	mapToState: func(b *client.DatabaseBinding, _ string, m *DatabaseBindingResourceModel) {
		mapDatabaseBindingToState(b, m)
	},
	timeoutsOf: func(m *DatabaseBindingResourceModel) timeouts.Value { return m.Timeouts },
	defaults: operationTimeouts{
		create: defaultDatabaseBindingCreateTimeout,
		delete: defaultDatabaseBindingDeleteTimeout,
	},
	ready: readiness[DatabaseBindingResourceModel, client.DatabaseBinding]{
		target: databaseBindingPhaseReady,
		phase:  databaseBindingPhase,
		converged: func(b *client.DatabaseBinding) bool {
			return databaseBindingPhase(b) == databaseBindingPhaseReady
		},
		// Waiting does not clear Failed: the tenant has to act.
		failed: func(b *client.DatabaseBinding) (time.Duration, bool) {
			return 0, databaseBindingPhase(b) == databaseBindingPhaseFailed
		},
		// Terraform taints a resource whose Create returns an error along
		// with a state, so the failed binding stays tracked: `terraform
		// destroy` removes it and the next apply replaces it.
		failedDetail: func(m *DatabaseBindingResourceModel, b *client.DatabaseBinding) string {
			return databaseBindingFailedDetail(m, b.Status)
		},
		timedOutDetail: func(m *DatabaseBindingResourceModel, b *client.DatabaseBinding) string {
			detail := fmt.Sprintf(" If the cluster %q does not exist yet, the binding becomes Ready once it does.",
				m.Cluster.ValueString())
			if b != nil && b.Status != nil && b.Status.Message != "" {
				detail += "\n\nStatus message: " + b.Status.Message
			}
			return detail
		},
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

func (r *DatabaseBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_binding"
}

func (r *DatabaseBindingResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Bindings are immutable: changing any argument replaces the binding.
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Binds a Kupe database into one namespace of one cluster for one role. The Kupe " +
			"platform delivers a Service named after the database and a credentials Secret into that namespace and " +
			"opens the network path; other namespaces see neither. Create waits for phase=Ready, and fails at once " +
			"if the binding is Failed: the namespace already has a Service named after the database, or a Secret " +
			"named `secret_name`, that the platform did not create. The failed binding stays in state, marked " +
			"tainted. Delete waits until the binding is gone. Set timeouts with the `timeouts` attribute, for " +
			"example `timeouts = { create = \"10m\" }` (defaults: create/delete 5m).\n\n" +
			"Bindings cannot be changed: changing any argument replaces the binding. Removing a binding removes the " +
			"Service, the Secret and the network path, but not knowledge of the password; rotate the role's " +
			"credentials if that matters.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Binding name. Defaults to `{database}-{cluster}-{namespace}-{role}`; set it explicitly " +
					"if that is longer than 63 characters.",
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
					"so Create waits up to its timeout and then warns.",
				Required:      true,
				Validators:    clusterRefValidators(),
				PlanModifiers: replace,
			},
			"namespace": schema.StringAttribute{
				Description:   "Namespace inside the cluster that receives the Service and credentials Secret. Not `kube-*` or `argocd`.",
				Required:      true,
				Validators:    bindingNamespaceValidators(),
				PlanModifiers: replace,
			},
			"role": schema.StringAttribute{
				Description:   "Role granted: `owner` (can change the schema) or `app` (reads and writes data).",
				Required:      true,
				PlanModifiers: replace,
				Validators: []validator.String{
					stringvalidator.OneOf("owner", "app"),
				},
			},
			"secret_name": schema.StringAttribute{
				Description: "Name of the credentials Secret in the namespace. When unset, a new binding uses " +
					"`{database}-{role}` and an existing or imported binding keeps its current name, so removing the " +
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
				Description: "Current phase: Pending (waiting for the database or the cluster), Ready, or Failed " +
					"(the namespace already has a Service or Secret of the same name that the platform did not create).",
				Computed: true,
			},
			"host": schema.StringAttribute{
				Description: "In-cluster DNS name applications connect to, e.g. `orders-db.api.svc.cluster.local`.",
				Computed:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
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
		Name:      plan.Name.ValueString(),
		Cluster:   plan.Cluster.ValueString(),
		Namespace: plan.Namespace.ValueString(),
		Role:      plan.Role.ValueString(),
	}
	if !plan.SecretName.IsUnknown() {
		createReq.SecretName = plan.SecretName.ValueString()
	}
	binding, err := r.client.CreateDatabaseBinding(ctx, plan.Database.ValueString(), createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create database binding", apiErrorDetail(err))
		return
	}
	r.created(ctx, binding, "", &plan, &resp.State, &resp.Diagnostics)
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

// databaseBindingFailedDetail explains a Failed binding: the platform's
// message, what to change when another object holds the binding's names, and
// what Terraform does with the failed binding next.
func databaseBindingFailedDetail(b *DatabaseBindingResourceModel, status *client.DatabaseBindingStatus) string {
	detail := fmt.Sprintf("Binding %q of database %q failed.", b.Name.ValueString(), b.Database.ValueString())
	if status != nil && status.Message != "" {
		detail += "\n\nStatus message: " + status.Message
	}
	if databaseBindingConflicted(status) {
		detail += fmt.Sprintf("\n\nNamespace %q of cluster %q already has a Service named %q or a Secret named %q "+
			"that the Kupe platform did not create, and the binding does not replace it. Rename or remove that "+
			"object, or, if the Secret is the clash, set a different `secret_name`.",
			b.Namespace.ValueString(), b.Cluster.ValueString(), b.Database.ValueString(), b.SecretName.ValueString())
	}
	return detail + "\n\nTerraform keeps the binding in state, marked tainted: the next `terraform apply` " +
		"replaces it and `terraform destroy` removes it."
}

// Update only runs when the `timeouts` attribute changes: every other argument
// requires replacement. It refreshes the binding and records the new timeouts.
func (r *DatabaseBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DatabaseBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.update(ctx, &plan, nil, "", nil, false, &resp.State, &resp.Diagnostics)
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

func mapDatabaseBindingToState(b *client.DatabaseBinding, state *DatabaseBindingResourceModel) {
	state.Name = types.StringValue(b.Name)
	state.Database = types.StringValue(b.Database)
	state.Cluster = types.StringValue(b.Cluster)
	state.Namespace = types.StringValue(b.Namespace)
	state.Role = types.StringValue(b.Role)

	phase, host, secretName := "", "", b.SecretName
	if b.Status != nil {
		phase, host = b.Status.Phase, b.Status.Host
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
		secretName = b.Database + "-" + b.Role
	}
	state.Phase = types.StringValue(phase)
	state.Host = types.StringValue(host)
	state.SecretName = types.StringValue(secretName)
}
