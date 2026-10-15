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
	_ resource.ResourceWithConfigure   = &DatabaseResource{}
	_ resource.ResourceWithImportState = &DatabaseResource{}
)

// DatabaseResource is kupe_database. Read, Delete, Configure and
// ImportState come from managedResource.
type DatabaseResource struct {
	managedResource[DatabaseResourceModel, client.Database]
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
	return &DatabaseResource{managedResource[DatabaseResourceModel, client.Database]{kind: &databaseKind}}
}

// databaseKind is the database's lifecycle for managedResource.
var databaseKind = serviceKind[DatabaseResourceModel, client.Database]{
	noun: "database",
	name: func(m *DatabaseResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *DatabaseResourceModel) (*client.Database, string, error) {
		return c.GetDatabase(ctx, m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *DatabaseResourceModel) error {
		return c.DeleteDatabase(ctx, m.Name.ValueString())
	},
	mapToState: mapDatabaseToState,
	timeoutsOf: func(m *DatabaseResourceModel) timeouts.Value { return m.Timeouts },
	defaults: operationTimeouts{
		create: defaultDatabaseCreateTimeout,
		update: defaultDatabaseUpdateTimeout,
		delete: defaultDatabaseDeleteTimeout,
	},
	ready: readiness[DatabaseResourceModel, client.Database]{
		target:    databasePhaseRunning,
		phase:     databasePhase,
		converged: databaseConverged,
	},
	settingsChanged: func(prior *DatabaseResourceModel, current *client.Database) []string {
		return settingsChanged(&prior.databaseModel, databaseModelFromAPI(current))
	},
	deleteRefused: func(m *DatabaseResourceModel, err error) (string, string, bool) {
		if !client.IsDeletionProtected(err) {
			return "", "", false
		}
		return "database has deletion protection enabled",
			fmt.Sprintf("Database %q was not deleted because deletion protection is enabled. To delete it, set "+
				"`deletion_protection = false` on the resource, run `terraform apply`, then destroy it again. %s",
				m.Name.ValueString(), databaseDeleteNotice), true
	},
	stillDeleting: func(m *DatabaseResourceModel) string {
		return fmt.Sprintf("DELETE was accepted but database %q is still terminating after the configured delete "+
			"timeout. Terraform has removed it from state; the Kupe platform finishes the teardown in the "+
			"background, and the name stays reserved until then (creating a database with the same name may "+
			"fail with a conflict). If it stays in this state, check it in the Kupe Console or contact Kupe "+
			"Cloud support. Override with `timeouts.delete = \"30m\"`.", m.Name.ValueString())
	},
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
			"resources": requiredResourcesAttribute("Per-instance resources in Kubernetes quantity format. Billing " +
				"uses these requested values. A high-availability database runs two instances of this size."),
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
			"etag":       etagAttribute(),
			"created_at": createdAtAttribute("the database"),
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
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
	r.created(ctx, db, etag, &plan, &resp.State, &resp.Diagnostics)
}

func (r *DatabaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DatabaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var send patchFunc[client.Database]
	if patch, hasChanges := databasePatch(&plan, &state); hasChanges {
		name := plan.Name.ValueString()
		send = func(ctx context.Context, etag string) (*client.Database, string, error) {
			return r.client.UpdateDatabase(ctx, name, etag, patch)
		}
	}
	r.update(ctx, &plan, &state, state.ETag.ValueString(), send, true, &resp.State, &resp.Diagnostics)
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

// databaseConverged reports whether the database is Running with its latest
// spec applied. Phase alone is not enough: a change that keeps the database
// Running (deletion protection, or the moment before a resize starts) is
// only applied once observedGeneration reaches generation.
func databaseConverged(db *client.Database) bool {
	return db.Status != nil &&
		db.Status.Phase == databasePhaseRunning &&
		db.Status.ObservedGeneration >= db.Generation
}

// databasePhase is db's phase, "" when it has no status yet.
func databasePhase(db *client.Database) string {
	if db == nil || db.Status == nil {
		return ""
	}
	return db.Status.Phase
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
