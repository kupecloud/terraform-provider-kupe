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

// databaseBindingPhaseReady is the phase Create waits for. Pending and
// Failed are treated as in progress: a binding to a cluster that does not
// exist yet becomes Ready once the cluster does.
const databaseBindingPhaseReady = "Ready"

// Default timeouts for binding operations, overridable in the `timeouts`
// block. Delivering a Service and Secret into a cluster takes seconds.
const (
	defaultDatabaseBindingCreateTimeout = 5 * time.Minute
	defaultDatabaseBindingDeleteTimeout = 5 * time.Minute
)

var (
	_ resource.Resource                = &DatabaseBindingResource{}
	_ resource.ResourceWithImportState = &DatabaseBindingResource{}
)

type DatabaseBindingResource struct {
	client *client.Client
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
	return &DatabaseBindingResource{}
}

func (r *DatabaseBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_binding"
}

func (r *DatabaseBindingResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Bindings are immutable: changing any argument replaces the binding.
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Binds a Kupe managed database into one namespace of one cluster for one role. The Kupe " +
			"platform delivers a Service named after the database and a credentials Secret into that namespace and " +
			"opens the network path; other namespaces see neither. Create waits for phase=Ready; Delete waits until " +
			"the binding is gone. Timeouts are configurable via the `timeouts` block (defaults: create/delete 5m).\n\n" +
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
				Validators:    databaseNameValidators(),
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
				Description: "Name of the credentials Secret in the namespace. Defaults to `{database}-{role}`. " +
					"The Secret holds `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending, Ready or Failed.",
				Computed:    true,
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

func (r *DatabaseBindingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	database := plan.Database.ValueString()
	binding, err := r.client.CreateDatabaseBinding(ctx, database, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create database binding", apiErrorDetail(err))
		return
	}

	// Record the binding before waiting so an interrupted apply still
	// tracks it.
	mapDatabaseBindingToState(binding, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultDatabaseBindingCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	message := ""
	err = pollUntil(ctx, timeout, func(c context.Context) (bool, error) {
		observed, err := r.client.GetDatabaseBinding(c, database, binding.Name)
		if err != nil {
			return false, err
		}
		mapDatabaseBindingToState(observed, &plan)
		resp.Diagnostics.Append(resp.State.Set(c, &plan)...)
		if observed.Status != nil {
			message = observed.Status.Message
		}
		return plan.Phase.ValueString() == databaseBindingPhaseReady, nil
	})
	switch {
	case err == nil:
		// Ready
	case isTerminalAPIError(err):
		resp.Diagnostics.AddError("database binding failed while waiting for Ready", apiErrorDetail(err))
	default:
		detail := fmt.Sprintf("Binding %q was at phase=%q when the create timeout fired. The Kupe platform "+
			"keeps reconciling it; re-run `terraform apply` later to record the Ready state. If the cluster %q "+
			"does not exist yet, the binding becomes Ready once it does.",
			binding.Name, plan.Phase.ValueString(), plan.Cluster.ValueString())
		if message != "" {
			detail += "\n\nStatus message: " + message
		}
		resp.Diagnostics.AddWarning("database binding create timed out before reaching Ready", detail)
	}
}

func (r *DatabaseBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DatabaseBindingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	binding, err := r.client.GetDatabaseBinding(ctx, state.Database.ValueString(), state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read database binding", apiErrorDetail(err))
		return
	}

	mapDatabaseBindingToState(binding, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only runs when the `timeouts` block changes: every other argument
// requires replacement. It refreshes the binding and records the new timeouts.
func (r *DatabaseBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DatabaseBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	binding, err := r.client.GetDatabaseBinding(ctx, plan.Database.ValueString(), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to read database binding", apiErrorDetail(err))
		return
	}
	mapDatabaseBindingToState(binding, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DatabaseBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DatabaseBindingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, defaultDatabaseBindingDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// One deadline covers both sending the delete and waiting for it.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	database, name := state.Database.ValueString(), state.Name.ValueString()
	if err := deleteUntilAccepted(ctx, timeout, func(c context.Context) error {
		return r.client.DeleteDatabaseBinding(c, database, name)
	}); err != nil {
		resp.Diagnostics.AddError("failed to delete database binding", deleteErrorDetail(err))
		return
	}

	err := waitForDeletion(ctx, timeout, func(c context.Context) error {
		_, err := r.client.GetDatabaseBinding(c, database, name)
		return err
	})
	switch {
	case err == nil:
		// confirmed gone
	case isTerminalAPIError(err):
		resp.Diagnostics.AddError("failed to confirm database binding deletion", apiErrorDetail(err))
	default:
		resp.Diagnostics.AddWarning(
			"database binding still terminating",
			fmt.Sprintf("DELETE was accepted but binding %q is still being removed after the configured delete "+
				"timeout. Terraform has removed it from state; the Kupe platform finishes removing the Service, "+
				"Secret and network access in the background.", name),
		)
	}
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
	state.Phase = types.StringValue(phase)
	state.Host = types.StringValue(host)
	state.SecretName = stringOrNull(secretName)
}
