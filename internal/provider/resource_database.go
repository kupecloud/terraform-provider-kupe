package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// databasePhaseRunning is the phase Create and Update wait for. Every other
// phase (Pending, Provisioning, Upgrading, Degraded) is in progress.
const databasePhaseRunning = "Running"

// databaseEngine is the only engine kupe offers today, and the default.
const databaseEngine = "postgres"

// databaseDeleteNotice is what deleting a database means for its data. Docs
// and diagnostics use the same words so the promise is stated once.
const databaseDeleteNotice = "Deleting a database removes it immediately. Its storage is kept for 7 days for " +
	"support-assisted recovery, then permanently deleted. There is no self-service restore in v1."

// Default timeouts for database operations, overridable in the `timeouts`
// block. Provisioning starts instances and storage; an upgrade or resize
// rolls every instance, so both get generous headroom.
const (
	defaultDatabaseCreateTimeout = 30 * time.Minute
	defaultDatabaseUpdateTimeout = 30 * time.Minute
	defaultDatabaseDeleteTimeout = 15 * time.Minute
)

var (
	_ resource.Resource                = &DatabaseResource{}
	_ resource.ResourceWithImportState = &DatabaseResource{}
)

type DatabaseResource struct {
	client *client.Client
}

// databaseModel holds the attributes the kupe_database resource and data
// source share.
type databaseModel struct {
	Name               types.String    `tfsdk:"name"`
	Engine             types.String    `tfsdk:"engine"`
	Version            types.String    `tfsdk:"version"`
	Resources          *ResourcesModel `tfsdk:"resources"`
	HighAvailability   types.Bool      `tfsdk:"high_availability"`
	DeletionProtection types.Bool      `tfsdk:"deletion_protection"`
	Phase              types.String    `tfsdk:"phase"`
	CurrentVersion     types.String    `tfsdk:"current_version"`
	CreatedAt          types.String    `tfsdk:"created_at"`
}

type DatabaseResourceModel struct {
	databaseModel
	ETag     types.String   `tfsdk:"etag"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func NewDatabaseResource() resource.Resource {
	return &DatabaseResource{}
}

func (r *DatabaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (r *DatabaseResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe PostgreSQL database. Create and Update wait for the database to " +
			"reach phase=Running with the change applied; Delete waits until the database is gone. Set timeouts " +
			"with the `timeouts` attribute, for example `timeouts = { create = \"45m\" }` (defaults: " +
			"create/update 30m, delete 15m).\n\n" +
			"Applications reach the database through `kupe_database_binding`, which places a Service and a " +
			"credentials Secret in one namespace of one cluster. Credentials are never returned by the API or " +
			"stored in Terraform state.\n\n" +
			"### Destroy semantics\n\n" +
			databaseDeleteNotice + " `deletion_protection` (default `true`) makes the Kupe platform " +
			"refuse the delete: set it to `false` and apply before destroying. Add `lifecycle { prevent_destroy = true }` " +
			"to stop Terraform from planning the destroy at all.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Database name (immutable after creation). A lowercase DNS label starting with a letter, " +
					"without `--`. It is also the Service name applications connect to.",
				Required:   true,
				Validators: databaseNameValidators(),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"engine": schema.StringAttribute{
				Description: "Database engine. Only `postgres` is supported, and it is the default. The engine of an " +
					"existing database cannot change: a different value fails the plan instead of replacing the database.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(databaseEngine),
				PlanModifiers: []planmodifier.String{
					rejectInPlaceChange("Database engine cannot be changed",
						"Create a new database with the engine you need and migrate the data."),
				},
			},
			"version": schema.StringAttribute{
				Description: "Engine major version, e.g. `18`. When unset, the platform default is used and recorded here. " +
					"Major version upgrades are not available yet, so changing the version of an existing database " +
					"fails the plan.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					// Keep the recorded version when config is unset so an
					// unrelated edit never sends a version.
					stringplanmodifier.UseStateForUnknown(),
					rejectInPlaceChange("Major version upgrades are not available yet",
						"Keep the current version, or create a new database with the version you need and migrate the data."),
				},
			},
			"resources": schema.SingleNestedAttribute{
				Description: "Per-instance resources in Kubernetes quantity format. Billing uses these requested " +
					"values. A high-availability database runs two instances of this size.",
				Required: true,
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Description: "CPU without a unit, e.g. `500m` or `2`.",
						Required:    true,
						Validators:  cpuQuantityValidators(),
					},
					"memory": schema.StringAttribute{
						Description: "Memory with a unit, e.g. `1Gi` or `512Mi`.",
						Required:    true,
						Validators:  sizeQuantityValidators(),
					},
					"storage": schema.StringAttribute{
						Description: "Storage with a unit, e.g. `10Gi`. Storage can only grow: a smaller value fails the plan.",
						Required:    true,
						Validators:  sizeQuantityValidators(),
						PlanModifiers: []planmodifier.String{
							storageNeverShrinks(),
						},
					},
				},
			},
			"high_availability": schema.BoolAttribute{
				Description: "Run two instances with a synchronous standby. Can be turned on or off in place. Default `false`.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"deletion_protection": schema.BoolAttribute{
				Description: "While `true`, the Kupe platform refuses to delete the database. Set it to `false` and " +
					"apply before destroying. Default `true`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending, Provisioning, Running, Upgrading, Degraded or Terminating.",
				Computed:    true,
			},
			"current_version": schema.StringAttribute{
				Description: "Full engine version running, e.g. `18.0`.",
				Computed:    true,
			},
			"etag": schema.StringAttribute{
				// No UseStateForUnknown: it changes on every write, so the
				// plan honestly says "known after apply" on any update.
				Description: "Resource version used for optimistic locking during updates.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the database was created.",
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

func (r *DatabaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *DatabaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DatabaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deletionProtection := plan.DeletionProtection.ValueBool()
	db, etag, err := r.client.CreateDatabase(ctx, client.CreateDatabaseRequest{
		Name:               plan.Name.ValueString(),
		Engine:             plan.Engine.ValueString(),
		Version:            plan.Version.ValueString(),
		Resources:          resourcesFromPlan(plan.Resources),
		HighAvailability:   plan.HighAvailability.ValueBool(),
		DeletionProtection: &deletionProtection,
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to create database", apiErrorDetail(err))
		return
	}

	// Record the database before waiting so an interrupted apply still
	// tracks it.
	mapDatabaseToState(db, etag, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultDatabaseCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitForDatabaseRunning(ctx, timeout, &plan, &resp.State, &resp.Diagnostics, "create")
}

func (r *DatabaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DatabaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	db, etag, err := r.client.GetDatabase(ctx, state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("failed to read database", apiErrorDetail(err))
		return
	}

	mapDatabaseToState(db, etag, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DatabaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DatabaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	patch, hasChanges := databasePatch(&plan, &state)
	name := plan.Name.ValueString()
	if !hasChanges {
		// Only provider-side settings (timeouts) changed: refresh.
		db, etag, err := r.client.GetDatabase(ctx, name)
		if err != nil {
			resp.Diagnostics.AddError("failed to read database", apiErrorDetail(err))
			return
		}
		mapDatabaseToState(db, etag, &plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	db, etag, err := r.updateDatabase(ctx, &state, patch)
	var changed *concurrentChangeError
	if errors.As(err, &changed) {
		resp.Diagnostics.AddError("database changed outside this apply",
			fmt.Sprintf("Database %q was changed by another client since Terraform last read it (%s now differs "+
				"from state), so the update was not applied. Run `terraform plan` to review the current values, then "+
				"apply again.", name, strings.Join(changed.fields, ", ")))
		return
	}
	var apiErr *client.APIError
	if client.IsPreconditionFailed(err) && errors.As(err, &apiErr) {
		// Only reached after the retry: the database kept changing between
		// the re-read and the second attempt.
		resp.Diagnostics.AddError("failed to update database", apiErrorSummary(apiErr)+
			"\n\nThe database kept changing while the update was being sent, so it was not applied. Run "+
			"`terraform apply` again.")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("failed to update database", apiErrorDetail(err))
		return
	}
	mapDatabaseToState(db, etag, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, defaultDatabaseUpdateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitForDatabaseRunning(ctx, timeout, &plan, &resp.State, &resp.Diagnostics, "update")
}

// databasePatch builds the merge patch for the fields that differ between
// plan and state. Version is only sent when the planned value is known, so
// an unset version can never reach the wire as "". The plan rejects version
// changes while major upgrades are unavailable; the API refuses them too.
func databasePatch(plan, state *DatabaseResourceModel) (client.PatchDatabaseRequest, bool) {
	var patch client.PatchDatabaseRequest
	hasChanges := false
	if !plan.Version.IsUnknown() && !plan.Version.IsNull() && !plan.Version.Equal(state.Version) {
		v := plan.Version.ValueString()
		patch.Version = &v
		hasChanges = true
	}
	if state.Resources == nil ||
		!plan.Resources.CPU.Equal(state.Resources.CPU) ||
		!plan.Resources.Memory.Equal(state.Resources.Memory) ||
		!plan.Resources.Storage.Equal(state.Resources.Storage) {
		patch.Resources = resourcesFromPlan(plan.Resources)
		hasChanges = true
	}
	if !plan.HighAvailability.Equal(state.HighAvailability) {
		ha := plan.HighAvailability.ValueBool()
		patch.HighAvailability = &ha
		hasChanges = true
	}
	if !plan.DeletionProtection.Equal(state.DeletionProtection) {
		protected := plan.DeletionProtection.ValueBool()
		patch.DeletionProtection = &protected
		hasChanges = true
	}
	return patch, hasChanges
}

// concurrentChangeError reports the fields another client changed while an
// update was in flight.
type concurrentChangeError struct {
	fields []string
}

func (e *concurrentChangeError) Error() string {
	return "database changed concurrently: " + strings.Join(e.fields, ", ")
}

// updateDatabase sends patch with the ETag from state. The ETag changes on
// every write, including the platform's own status updates, so a 412 often
// means only the status moved. On a 412 it re-reads the database: when its
// settings still match state, nobody else changed them and the patch is sent
// once more with the fresh ETag; otherwise it returns a
// *concurrentChangeError. Every setting is compared, not only the patched
// ones, because the response to the retried patch becomes the new state and
// must match the plan.
func (r *DatabaseResource) updateDatabase(
	ctx context.Context, state *DatabaseResourceModel, patch client.PatchDatabaseRequest,
) (*client.Database, string, error) {
	name := state.Name.ValueString()
	db, etag, err := r.client.UpdateDatabase(ctx, name, state.ETag.ValueString(), patch)
	if !client.IsPreconditionFailed(err) {
		return db, etag, err
	}
	current, currentETag, getErr := r.client.GetDatabase(ctx, name)
	if getErr != nil {
		return nil, "", fmt.Errorf("re-reading database after a stale update: %w", getErr)
	}
	if fields := settingsChanged(&state.databaseModel, databaseModelFromAPI(current)); len(fields) > 0 {
		return nil, "", &concurrentChangeError{fields: fields}
	}
	return r.client.UpdateDatabase(ctx, name, currentETag, patch)
}

// settingsChanged lists, by attribute name, the settings an update can
// change whose current value differs from prior.
func settingsChanged(prior *databaseModel, current databaseModel) []string {
	var changed []string
	if !current.Version.Equal(prior.Version) {
		changed = append(changed, "version")
	}
	if !resourcesEqual(prior.Resources, current.Resources) {
		changed = append(changed, "resources")
	}
	if !current.HighAvailability.Equal(prior.HighAvailability) {
		changed = append(changed, "high_availability")
	}
	if !current.DeletionProtection.Equal(prior.DeletionProtection) {
		changed = append(changed, "deletion_protection")
	}
	return changed
}

func resourcesEqual(a, b *ResourcesModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.CPU.Equal(b.CPU) && a.Memory.Equal(b.Memory) && a.Storage.Equal(b.Storage)
}

func (r *DatabaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DatabaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, defaultDatabaseDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// One deadline covers both sending the delete and waiting for it.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := state.Name.ValueString()
	err := deleteUntilAccepted(ctx, timeout, func(c context.Context) error {
		return r.client.DeleteDatabase(c, name)
	})
	if client.IsDeletionProtected(err) {
		resp.Diagnostics.AddError("database has deletion protection enabled",
			fmt.Sprintf("Database %q was not deleted because deletion protection is enabled. To delete it, set "+
				"`deletion_protection = false` on the resource, run `terraform apply`, then destroy it again. %s",
				name, databaseDeleteNotice))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("failed to delete database", deleteErrorDetail(err))
		return
	}

	err = waitForDeletion(ctx, timeout, func(c context.Context) error {
		_, _, err := r.client.GetDatabase(c, name)
		return err
	})
	switch {
	case err == nil:
		// confirmed gone
	case isTerminalAPIError(err):
		resp.Diagnostics.AddError("failed to confirm database deletion", apiErrorDetail(err))
	default:
		resp.Diagnostics.AddWarning(
			"database still terminating",
			fmt.Sprintf("DELETE was accepted but database %q is still terminating after the configured delete "+
				"timeout. Terraform has removed it from state; the Kupe platform finishes the teardown in the "+
				"background, and the name stays reserved until then (creating a database with the same name may "+
				"fail with a conflict). If it stays in this state, check it in the Kupe Console or contact Kupe "+
				"Cloud support. Override with `timeouts.delete = \"30m\"`.", name),
		)
	}
}

func (r *DatabaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// waitForDatabaseRunning polls until databaseConverged or the timeout,
// saving each observation to state. kind ("create" or "update") names the
// operation in diagnostics.
func (r *DatabaseResource) waitForDatabaseRunning(
	ctx context.Context,
	timeout time.Duration,
	plan *DatabaseResourceModel,
	state *tfsdk.State,
	diags *diag.Diagnostics,
	kind string,
) {
	name := plan.Name.ValueString()
	err := pollUntil(ctx, timeout, func(c context.Context) (bool, error) {
		db, etag, err := r.client.GetDatabase(c, name)
		if err != nil {
			return false, err
		}
		mapDatabaseToState(db, etag, plan)
		diags.Append(state.Set(c, plan)...)
		return databaseConverged(db), nil
	})
	switch {
	case err == nil:
		// converged
	case isTerminalAPIError(err):
		diags.AddError(fmt.Sprintf("database %s failed while waiting for Running", kind), apiErrorDetail(err))
	default:
		diags.AddWarning(
			fmt.Sprintf("database %s timed out before reaching Running", kind),
			fmt.Sprintf("Database %q was at phase=%q when the %s timeout fired. The Kupe platform continues in "+
				"the background; re-run `terraform apply` later and it will be a no-op once the database is "+
				"Running. If it stays in this phase, check it in the Kupe Console or contact Kupe Cloud support. "+
				"Override with `timeouts.%s = \"60m\"`.", name, plan.Phase.ValueString(), kind, kind),
		)
	}
}

// databaseConverged reports whether the database is Running with its latest
// spec applied. Phase alone is not enough: a change that keeps the database
// Running (deletion protection, or the moment before a resize starts) is
// only applied once observedGeneration reaches generation.
func databaseConverged(db *client.Database) bool {
	return db.Status != nil &&
		db.Status.Phase == databasePhaseRunning &&
		db.Status.ObservedGeneration >= db.Generation
}

func mapDatabaseToState(db *client.Database, etag string, state *DatabaseResourceModel) {
	state.databaseModel = databaseModelFromAPI(db)
	state.ETag = types.StringValue(etag)
}

func databaseModelFromAPI(db *client.Database) databaseModel {
	resources := &ResourcesModel{CPU: types.StringNull(), Memory: types.StringNull(), Storage: types.StringNull()}
	if db.Resources != nil {
		resources = &ResourcesModel{
			CPU:     stringOrNull(db.Resources.CPU),
			Memory:  stringOrNull(db.Resources.Memory),
			Storage: stringOrNull(db.Resources.Storage),
		}
	}
	phase, currentVersion := "", ""
	if db.Status != nil {
		phase, currentVersion = db.Status.Phase, db.Status.CurrentVersion
	}
	return databaseModel{
		Name:               types.StringValue(db.Name),
		Engine:             types.StringValue(db.Engine),
		Version:            types.StringValue(db.Version),
		Resources:          resources,
		HighAvailability:   types.BoolValue(db.HighAvailability),
		DeletionProtection: types.BoolValue(db.DeletionProtection),
		Phase:              types.StringValue(phase),
		CurrentVersion:     types.StringValue(currentVersion),
		CreatedAt:          types.StringValue(db.CreatedAt),
	}
}
