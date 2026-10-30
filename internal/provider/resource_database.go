package provider

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Database phases Create and Update act on. Running is the one they wait
// for; Pending and Provisioning are in progress; Terminating ends the wait
// at once and Degraded, once the platform has seen the change, after it has
// lasted databaseDegradedGrace.
const (
	databasePhaseRunning  = "Running"
	databasePhaseDegraded = "Degraded"
)

// databaseDegradedGrace is how long Create and Update ride out a Degraded
// database before failing. A single-instance database is Degraded while its
// only instance restarts to apply a CPU or memory change; kupe-cli waits
// the same 5 minutes. A var so tests can shorten it.
var databaseDegradedGrace = 5 * time.Minute

// databaseEngine is the only engine kupe offers today, and the default.
const databaseEngine = "postgres"

// databaseDeleteNotice is what deleting a database means for its data. Docs
// and diagnostics use the same words so the promise is stated once.
const databaseDeleteNotice = "Deleting a database removes it and its automatic backups immediately, and they cannot be restored. " +
	"Manual backups stay until you delete them."

// Backup schedules the platform offers. The platform also publishes the
// retention range it accepts and refuses a value outside it, so the schema
// checks only that a retention is a positive number of days.
const (
	backupScheduleDaily           = "Daily"
	backupScheduleDailyThenWeekly = "DailyThenWeekly"
)

// Descriptions of the backup attributes. The resource and the data source
// share the status ones.
const (
	backupDescription = "Backup settings: how far back the backups reach and how many full backups are kept. " +
		"Fields left out keep their current value, and removing `backup` from the configuration keeps the " +
		"current settings. Null on a database that has never set them: the defaults apply, 7 days and `Daily`. " +
		"While the Kupe platform does not offer backups, setting `backup` fails with error code " +
		"`backups_unavailable`."
	backupRetentionDescription = "How far back the backups reach, in days: 7 to 35, default 7. The Kupe platform " +
		"sets the range and refuses a value outside it (error code `DATABASE_BACKUP_RETENTION_OUT_OF_RANGE`). " +
		"Shortening retention deletes the backups that fall outside the new window."
	backupScheduleDescription = "How many full backups are kept within the retention period: `Daily` keeps one " +
		"for every day; `DailyThenWeekly` keeps one for each of the last 7 days and one a week before that. The " +
		"points you can restore to are the same either way; restoring to an older point takes longer. Default " +
		"`Daily`."
	lastBackupAtDescription = "When the newest full backup finished (RFC 3339). Empty until the first backup " +
		"completes."
	recoveryWindowDescription = "The restore window: the oldest and newest points you can restore the database " +
		"to. Null until the backups report them."
	recoveryWindowEarliestDescription = "The oldest point you can restore to (RFC 3339)."
	recoveryWindowLatestDescription   = "The newest point you can restore to: the newest WAL saved (RFC 3339)."
	backupSizeBytesDescription        = "Bytes every full backup and all WAL kept for the database take up in " +
		"storage, manual backups included. 0 until measured."
)

// Descriptions of the `source` attributes.
const (
	sourceDescription = "Restore the new database from a manual backup or from another database, instead of " +
		"starting it empty. Set exactly one of `backup` and `database`. The new database runs the source's major " +
		"version: leave `version` unset, or set that one. Its storage must be at least the source's. The source " +
		"is unchanged, and the new database counts against the plan's database limit like any other. `source` " +
		"is read only when the database is created, and any change to it replaces the database: Terraform " +
		"deletes it and restores a new one. That includes a value Terraform knows only at apply time, such as " +
		"the name of a `kupe_database_backup` without `name` that is taken again. Once the database exists, " +
		"remove `source` from the configuration (the database keeps it), or keep it constant: name the backup " +
		"with a literal or a backup resource that sets `name`, and write `point_in_time` literally. Null on a " +
		"database that was not restored. While the Kupe platform does not offer backups, setting `source` " +
		"fails with error code `backups_unavailable`."
	sourceBackupDescription = "Name of a manual backup to restore. The database it was taken of may have been " +
		"deleted. The new database holds the data as it was when the backup completed."
	sourceDatabaseDescription = "Name of a database to restore. The new database holds its data as it was at " +
		"`point_in_time`, or at the latest point when that is unset."
	sourcePointInTimeDescription = "The moment to restore `database` to, in UTC with whole seconds, for example " +
		"`2026-10-30T09:00:00Z`. It must be inside that database's restore window (its `recovery_window`). " +
		"Leave it unset to restore to the latest point. Only with `database`. Write it literally: a time computed " +
		"when Terraform plans, such as `timestamp()`, changes on every plan and replaces the database each time."
)

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
	// Backup and RecoveryWindow are objects, not struct pointers like
	// Resources: a plan can hold them unknown (Backup on create without a
	// `backup` attribute, RecoveryWindow on every change), which a struct
	// pointer cannot represent.
	Backup types.Object `tfsdk:"backup"`
	// Source is null on a database that was not restored.
	Source          types.Object `tfsdk:"source"`
	Phase           types.String `tfsdk:"phase"`
	CurrentVersion  types.String `tfsdk:"current_version"`
	LastBackupAt    types.String `tfsdk:"last_backup_at"`
	RecoveryWindow  types.Object `tfsdk:"recovery_window"`
	BackupSizeBytes types.Int64  `tfsdk:"backup_size_bytes"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// databaseBackupAttrTypes are the attributes of `backup`.
var databaseBackupAttrTypes = map[string]attr.Type{
	"retention": types.Int64Type,
	"schedule":  types.StringType,
}

// databaseSourceAttrTypes are the attributes of `source`.
var databaseSourceAttrTypes = map[string]attr.Type{
	"backup":        types.StringType,
	"database":      types.StringType,
	"point_in_time": types.StringType,
}

// recoveryWindowAttrTypes are the attributes of `recovery_window`.
var recoveryWindowAttrTypes = map[string]attr.Type{
	"earliest": types.StringType,
	"latest":   types.StringType,
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
		failed: func(db *client.Database) (time.Duration, bool) {
			switch databasePhase(db) {
			case phaseTerminating:
				return 0, true
			case databasePhaseDegraded:
				// Degraded from before the change (the first poll reads the
				// status the PATCH found) is not a verdict on it.
				return databaseDegradedGrace, db.Status.ObservedGeneration >= db.Generation
			}
			return 0, false
		},
		message: func(db *client.Database) string {
			if db == nil || db.Status == nil {
				return ""
			}
			return client.ConditionMessage(db.Status.Conditions, client.ConditionReady)
		},
		notApplied: func(db *client.Database) bool {
			return db != nil && db.Status != nil &&
				reconcileFailing(db.Generation, db.Status.ObservedGeneration, db.Status.Conditions)
		},
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
			"reach phase=Running with the change in effect on every instance. They fail at once if the database " +
			"is being deleted (Terminating), and after 5 minutes if it stays Degraded once the platform has " +
			"picked up the change. If the timeout fires first, the apply fails; the platform carries on in the " +
			"background, and a database that timed out on Create stays in state, marked tainted. A change to " +
			"`deletion_protection` alone takes effect at once and is not waited for. Delete waits until the " +
			"database is gone. Set timeouts " +
			"with the `timeouts` attribute, for example `timeouts = { create = \"45m\" }` (defaults: " +
			"create/update 30m, delete 15m).\n\n" +
			"Applications reach the database through `kupe_database_binding`, which places a Service and a " +
			"credentials Secret in each namespace it lists, in one cluster. Credentials are never returned by the API or " +
			"stored in Terraform state.\n\n" +
			"### Destroy semantics\n\n" +
			databaseDeleteNotice + " `deletion_protection` (default `true`) makes the Kupe platform " +
			"refuse the delete: set it to `false` and apply before destroying. Add `lifecycle { prevent_destroy = true }` " +
			"to stop Terraform from planning the destroy at all.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Database name (immutable after creation). A lowercase DNS label starting with a letter, " +
					"without `--`, at most 44 characters. It is also the Service name applications connect to. The Kupe " +
					"platform also limits it to 56 characters minus the length of the tenant name, so tenant names over " +
					"12 characters allow fewer than 44; the provider cannot check that at plan time, so the platform " +
					"refuses a longer name when the database is created.",
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
				Description: "Engine major version, e.g. `18`. When unset, the platform default is used and recorded here; " +
					"a database restored with `source` runs the source's major version. " +
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
				"uses these requested values. A high-availability database runs two instances of this size. All three " +
				"are required, unlike the optional limits of `kupe_cluster`."),
			"high_availability": schema.BoolAttribute{
				Description: "Run two instances with a synchronous standby. Can be turned on or off in place. When " +
					"unset, a new database is created without it and an existing or imported database keeps its " +
					"current setting, so removing the attribute from configuration does not turn HA off: set it to " +
					"`false` to do that.",
				Optional: true,
				Computed: true,
				// No default: a schema default would apply to an unset
				// attribute even when state holds a value, so importing an
				// HA database would plan turning HA off. Create sends no
				// value and the API defaults it to false.
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"deletion_protection": schema.BoolAttribute{
				Description: "While `true`, the Kupe platform refuses to delete the database. Set it to `false` and " +
					"apply before destroying. Default `true`, also for an existing database: importing an unprotected " +
					"database without this attribute in configuration plans turning protection on.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"backup": schema.SingleNestedAttribute{
				Description: backupDescription,
				Optional:    true,
				Computed:    true,
				// Unset in configuration, the recorded settings stay: the
				// platform never changes them on its own, and a database
				// that never set them stays null.
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.UseStateForUnknown(),
				},
				Attributes: map[string]schema.Attribute{
					"retention": schema.Int64Attribute{
						Description: backupRetentionDescription,
						Optional:    true,
						Computed:    true,
						// Non-null only: a database without backup settings
						// that gains a `backup` attribute gets the platform's
						// values for the fields it leaves out.
						PlanModifiers: []planmodifier.Int64{
							int64planmodifier.UseNonNullStateForUnknown(),
						},
						Validators: []validator.Int64{
							int64validator.AtLeast(1),            // codespell:ignore
							int64validator.AtMost(math.MaxInt32), // codespell:ignore
							// `backup = {}` has nothing to send on PATCH, so a never-set database's unknown children never resolve.
							int64validator.AtLeastOneOf(path.MatchRoot("backup").AtName("schedule")),
						},
					},
					"schedule": schema.StringAttribute{
						Description: backupScheduleDescription,
						Optional:    true,
						Computed:    true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseNonNullStateForUnknown(),
						},
						Validators: []validator.String{
							stringvalidator.OneOf(backupScheduleDaily, backupScheduleDailyThenWeekly),
						},
					},
				},
			},
			"source": schema.SingleNestedAttribute{
				Description: sourceDescription,
				Optional:    true,
				// Computed, with computed fields, so that removing `source`
				// from the configuration, or importing a restored database,
				// keeps what the API echoes instead of planning a
				// replacement. sourcePlan keeps a configured `source` exactly
				// as configured.
				Computed: true,
				PlanModifiers: []planmodifier.Object{
					sourcePlan(),
					objectplanmodifier.RequiresReplace(),
				},
				Attributes: map[string]schema.Attribute{
					"backup": schema.StringAttribute{
						Description: sourceBackupDescription,
						Optional:    true,
						Computed:    true,
						Validators: append(backupNameValidators(),
							stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("database"))),
					},
					"database": schema.StringAttribute{
						Description: sourceDatabaseDescription,
						Optional:    true,
						Computed:    true,
						Validators:  databaseRefValidators(),
					},
					"point_in_time": schema.StringAttribute{
						Description: sourcePointInTimeDescription,
						Optional:    true,
						Computed:    true,
						Validators: []validator.String{
							utcSecondsTime{},
							stringvalidator.AlsoRequires(path.MatchRelative().AtParent().AtName("database")),
						},
					},
				},
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending, Provisioning, Running, Degraded or Terminating.",
				Computed:    true,
			},
			"current_version": schema.StringAttribute{
				Description: "Full engine version running, e.g. `18.0`.",
				Computed:    true,
			},
			"last_backup_at": schema.StringAttribute{
				Description: lastBackupAtDescription,
				Computed:    true,
			},
			"recovery_window": schema.SingleNestedAttribute{
				Description: recoveryWindowDescription,
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"earliest": schema.StringAttribute{
						Description: recoveryWindowEarliestDescription,
						Computed:    true,
					},
					"latest": schema.StringAttribute{
						Description: recoveryWindowLatestDescription,
						Computed:    true,
					},
				},
			},
			"backup_size_bytes": schema.Int64Attribute{
				Description: backupSizeBytesDescription,
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
		Backup:             backupOnCreate(plan.Backup),
		Source:             sourceOnCreate(plan.Source),
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
	patch, hasChanges := databasePatch(&plan, &state)
	if hasChanges {
		name := plan.Name.ValueString()
		send = func(ctx context.Context, etag string) (*client.Database, string, error) {
			return r.client.UpdateDatabase(ctx, name, etag, patch)
		}
	}
	r.update(ctx, &plan, &state, state.ETag.ValueString(), send, !protectionOnly(patch), &resp.State, &resp.Diagnostics)
}

// protectionOnly reports whether patch changes deletion protection and
// nothing else. kupe-api and the platform read deletion protection from the
// spec when a delete arrives, so it is in effect as soon as the PATCH is
// accepted: Update does not wait for it. Every other PatchDatabaseRequest
// field must be checked here (TestProtectionOnlyCoversEveryPatchField).
func protectionOnly(patch client.PatchDatabaseRequest) bool {
	return patch.DeletionProtection != nil &&
		patch.Version == nil && patch.Resources == nil && patch.HighAvailability == nil && patch.Backup == nil
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
	if backup := backupChanges(plan.Backup, state.Backup); backup != nil {
		patch.Backup = backup
		hasChanges = true
	}
	return patch, hasChanges
}

// backupOnCreate is the backup settings a new database is created with: the
// planned keys with a known value, nil when `backup` is unset so the
// platform's defaults apply. `backup = {}` is refused at plan time.
func backupOnCreate(plan types.Object) *client.DatabaseBackup {
	if !isSet(plan) {
		return nil
	}
	return backupChanges(plan, types.ObjectNull(databaseBackupAttrTypes))
}

// sourceOnCreate is what a new database is restored from: the planned
// `source` keys that are set, nil when `source` is unset.
func sourceOnCreate(plan types.Object) *client.DatabaseSource {
	if !isSet(plan) {
		return nil
	}
	attrs := plan.Attributes()
	value := func(key string) string {
		v, _ := attrs[key].(types.String)
		return v.ValueString()
	}
	return &client.DatabaseSource{
		Backup:      value("backup"),
		Database:    value("database"),
		PointInTime: value("point_in_time"),
	}
}

// backupChanges is the backup settings to send: the planned keys with a
// known value that differs from prior's. nil when none does, so an unchanged
// `backup` never reaches the wire: kupe-api refuses any `backup` while the
// platform does not offer backups, and an empty one on PATCH.
func backupChanges(plan, prior types.Object) *client.DatabaseBackup {
	retention, schedule := backupAttributes(plan)
	priorRetention, priorSchedule := backupAttributes(prior)
	var backup client.DatabaseBackup
	if isSet(retention) && !retention.Equal(priorRetention) {
		backup.Retention = retentionDays(retention.ValueInt64())
	}
	if isSet(schedule) && !schedule.Equal(priorSchedule) {
		value := schedule.ValueString()
		backup.Schedule = &value
	}
	if backup.Retention == nil && backup.Schedule == nil {
		return nil
	}
	return &backup
}

// backupAttributes is the retention and schedule of a `backup` object:
// unknown when it is unknown, null when it is null.
func backupAttributes(backup types.Object) (types.Int64, types.String) {
	if backup.IsUnknown() {
		return types.Int64Unknown(), types.StringUnknown()
	}
	if backup.IsNull() {
		return types.Int64Null(), types.StringNull()
	}
	attrs := backup.Attributes()
	retention, _ := attrs["retention"].(types.Int64)
	schedule, _ := attrs["schedule"].(types.String)
	return retention, schedule
}

// retentionDays is a retention the schema validated (1 to math.MaxInt32
// days) as the API's int32.
func retentionDays(days int64) *int32 {
	if days < 1 || days > math.MaxInt32 {
		return nil
	}
	d := int32(days)
	return &d
}

// isSet reports whether v is known and not null.
func isSet(v attr.Value) bool {
	return !v.IsNull() && !v.IsUnknown()
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
	if !current.Backup.Equal(prior.Backup) {
		changed = append(changed, "backup")
	}
	return changed
}

// databaseConverged reports whether the database is Running with its latest
// spec in effect. Phase alone is not enough: the platform marks a change
// applied (observedGeneration reaches generation) as soon as it hands it to
// the instances, and the phase stays Running while they roll onto it, with
// the Ready condition False until they have.
func databaseConverged(db *client.Database) bool {
	return db.Status != nil &&
		db.Status.Phase == databasePhaseRunning &&
		db.Status.ObservedGeneration >= db.Generation &&
		db.Status.Ready()
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
	status := db.Status
	if status == nil {
		status = &client.DatabaseStatus{}
	}
	return databaseModel{
		Name:               types.StringValue(db.Name),
		Engine:             types.StringValue(db.Engine),
		Version:            types.StringValue(db.Version),
		Resources:          resources,
		HighAvailability:   types.BoolValue(db.HighAvailability),
		DeletionProtection: types.BoolValue(db.DeletionProtection),
		Backup:             backupFromAPI(db.Backup),
		Source:             sourceFromAPI(db.Source),
		Phase:              types.StringValue(status.Phase),
		CurrentVersion:     types.StringValue(status.CurrentVersion),
		LastBackupAt:       types.StringValue(status.LastBackupAt),
		RecoveryWindow:     recoveryWindowFromAPI(status.RecoveryWindow),
		BackupSizeBytes:    types.Int64Value(status.BackupSizeBytes),
		CreatedAt:          types.StringValue(db.CreatedAt),
	}
}

// backupFromAPI is the `backup` object for the API's settings: null when the
// database never set them, and a null field for a key never set.
func backupFromAPI(backup *client.DatabaseBackup) types.Object {
	if backup == nil {
		return types.ObjectNull(databaseBackupAttrTypes)
	}
	retention, schedule := types.Int64Null(), types.StringNull()
	if backup.Retention != nil {
		retention = types.Int64Value(int64(*backup.Retention))
	}
	if backup.Schedule != nil {
		schedule = types.StringValue(*backup.Schedule)
	}
	return types.ObjectValueMust(databaseBackupAttrTypes, map[string]attr.Value{
		"retention": retention,
		"schedule":  schedule,
	})
}

// sourceFromAPI is the `source` object for what the API echoes a restored
// database was restored from: null for a database that was not restored,
// and a null field for a key it does not send.
func sourceFromAPI(source *client.DatabaseSource) types.Object {
	if source == nil {
		return types.ObjectNull(databaseSourceAttrTypes)
	}
	return types.ObjectValueMust(databaseSourceAttrTypes, map[string]attr.Value{
		"backup":        stringOrNull(source.Backup),
		"database":      stringOrNull(source.Database),
		"point_in_time": stringOrNull(source.PointInTime),
	})
}

// recoveryWindowFromAPI is the `recovery_window` object, null until the
// backups report one.
func recoveryWindowFromAPI(window *client.RecoveryWindow) types.Object {
	if window == nil {
		return types.ObjectNull(recoveryWindowAttrTypes)
	}
	return types.ObjectValueMust(recoveryWindowAttrTypes, map[string]attr.Value{
		"earliest": types.StringValue(window.Earliest),
		"latest":   types.StringValue(window.Latest),
	})
}
