package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Backup phases Create acts on. Completed is the one it waits for; Failed
// and Deleting end the wait at once: waiting does not clear either.
const (
	backupPhaseCompleted = "Completed"
	backupPhaseFailed    = "Failed"
	backupPhaseDeleting  = "Deleting"
)

// Default timeouts for backup operations, overridable in the `timeouts`
// block. A backup copies the whole database, so a large one takes a while.
const (
	defaultDatabaseBackupCreateTimeout = 60 * time.Minute
	defaultDatabaseBackupDeleteTimeout = 15 * time.Minute
)

// Descriptions the resource and the data source share.
const (
	backupPhaseDescription = "The backup's state: Pending, Running, Completed, Failed or Deleting. `Completed` " +
		"once it can be restored from."
	backupCompletedAtDescription     = "When the backup finished (RFC 3339). Empty until it completes."
	backupSizeDescription            = "Size of the backup in bytes. 0 until measured."
	backupSourceVersionDescription   = "PostgreSQL major version of the database when the backup was taken. A database restored from the backup runs this version. Empty until the backup starts."
	backupSourceStorageDescription   = "Storage the database had when the backup was taken. A database restored from the backup needs at least this much. Empty until the backup starts."
	backupDatabaseDeletedDescription = "True once the database the backup was taken of is deleted or being deleted. " +
		"The backup stays until you delete it."
)

var (
	_ resource.Resource                = &DatabaseBackupResource{}
	_ resource.ResourceWithConfigure   = &DatabaseBackupResource{}
	_ resource.ResourceWithImportState = &DatabaseBackupResource{}
)

// DatabaseBackupResource is kupe_database_backup. Read, Delete, Configure
// and ImportState (by backup name) come from managedResource.
type DatabaseBackupResource struct {
	managedResource[DatabaseBackupResourceModel, client.Backup]
}

// databaseBackupModel holds the attributes the kupe_database_backup
// resource and data source share.
type databaseBackupModel struct {
	Name            types.String `tfsdk:"name"`
	Database        types.String `tfsdk:"database"`
	Phase           types.String `tfsdk:"phase"`
	CompletedAt     types.String `tfsdk:"completed_at"`
	SizeBytes       types.Int64  `tfsdk:"size_bytes"`
	SourceVersion   types.String `tfsdk:"source_version"`
	SourceStorage   types.String `tfsdk:"source_storage"`
	DatabaseDeleted types.Bool   `tfsdk:"database_deleted"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

type DatabaseBackupResourceModel struct {
	databaseBackupModel
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func NewDatabaseBackupResource() resource.Resource {
	return &DatabaseBackupResource{
		managedResource[DatabaseBackupResourceModel, client.Backup]{kind: &databaseBackupKind},
	}
}

// databaseBackupKind is the backup's lifecycle for managedResource. A backup
// cannot change, so there is no update to retry.
var databaseBackupKind = serviceKind[DatabaseBackupResourceModel, client.Backup]{
	noun: "database backup",
	name: func(m *DatabaseBackupResourceModel) string { return m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *DatabaseBackupResourceModel) (*client.Backup, string, error) {
		return c.GetBackup(ctx, m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *DatabaseBackupResourceModel) error {
		return c.DeleteBackup(ctx, m.Name.ValueString())
	},
	mapToState: func(b *client.Backup, _ string, m *DatabaseBackupResourceModel) {
		m.databaseBackupModel = databaseBackupModelFromAPI(b)
	},
	timeoutsOf: func(m *DatabaseBackupResourceModel) timeouts.Value { return m.Timeouts },
	defaults: operationTimeouts{
		create: defaultDatabaseBackupCreateTimeout,
		delete: defaultDatabaseBackupDeleteTimeout,
	},
	ready: readiness[DatabaseBackupResourceModel, client.Backup]{
		target:    backupPhaseCompleted,
		phase:     backupPhase,
		converged: func(b *client.Backup) bool { return backupPhase(b) == backupPhaseCompleted },
		failed: func(b *client.Backup) (time.Duration, bool) {
			phase := backupPhase(b)
			return 0, phase == backupPhaseFailed || phase == backupPhaseDeleting
		},
		message:      backupMessage,
		failedDetail: databaseBackupFailedDetail,
	},
	stillDeleting: func(m *DatabaseBackupResourceModel) string {
		return fmt.Sprintf("DELETE was accepted but backup %q is still being deleted after the configured delete "+
			"timeout. Terraform has removed it from state; the Kupe platform finishes deleting it in the "+
			"background, and the name stays reserved until then. Override with `timeouts.delete = \"30m\"`.",
			m.Name.ValueString())
	},
}

// backupPhase is b's phase, "" when it has no status yet.
func backupPhase(b *client.Backup) string {
	if b == nil || b.Status == nil {
		return ""
	}
	return b.Status.Phase
}

// backupMessage is what b's Ready condition says: what it waits for, or why
// it failed.
func backupMessage(b *client.Backup) string {
	if b == nil || b.Status == nil {
		return ""
	}
	return client.ConditionMessage(b.Status.Conditions, client.ConditionReady)
}

// databaseBackupFailedDetail explains a backup that failed or is being
// deleted while Create waited for it. Create is the only operation that
// waits, and Terraform keeps the backup in state, marked tainted.
func databaseBackupFailedDetail(_ string, m *DatabaseBackupResourceModel, b *client.Backup) string {
	name := m.Name.ValueString()
	if backupPhase(b) == backupPhaseDeleting {
		return fmt.Sprintf("Backup %q is being deleted, so it did not complete. Once it is gone, `terraform "+
			"apply` takes it again; remove it from the configuration if that is not wanted.", name)
	}
	detail := fmt.Sprintf("Backup %q of database %q failed.", name, m.Database.ValueString())
	if msg := backupMessage(b); msg != "" {
		detail += "\n\nStatus message: " + msg
	}
	return detail + "\n\nTerraform keeps the backup in state, marked tainted: the next `terraform apply` " +
		"deletes it and takes a new one, and `terraform destroy` deletes it."
}

func (r *DatabaseBackupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_backup"
}

func (r *DatabaseBackupResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Every argument replaces the backup when it changes: a backup cannot
	// change, so a change takes a new one.
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Takes a manual backup of a Kupe database. Create waits until the backup has completed " +
			"(phase `Completed`) and fails at once if it fails. A backup that fails or times out on create stays " +
			"in state, marked tainted. Changing `name` or `database` takes a new backup. Set timeouts with the " +
			"`timeouts` attribute, for example `timeouts = { create = \"90m\" }` (defaults: create 60m, delete " +
			"15m).\n\n" +
			"A manual backup is kept until you delete it, even after the database is deleted, and a new database " +
			"can be restored from it with `source.backup` on `kupe_database`. It is deleted with your account. A " +
			"database can keep up to 20 manual backups. Reading and deleting a backup works even while the Kupe " +
			"platform has databases or backups switched off.\n\n" +
			"### Destroy semantics\n\n" +
			"Destroying this resource deletes the backup: it can no longer be restored from. " + databaseDeleteNotice,
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Backup name: lowercase letters, digits and `-`, starting and ending with a letter or " +
					"digit, at most 63 characters. When unset, the Kupe platform names the backup after the database " +
					"and the time, in UTC, for example `orders-db-20261030-141500`. Changing it takes a new backup.",
				Optional:   true,
				Computed:   true,
				Validators: backupNameValidators(),
				PlanModifiers: []planmodifier.String{
					// Unset in configuration, the name the platform chose
					// stays, so a later plan never replaces the backup.
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"database": schema.StringAttribute{
				Description: "Name of the database to back up. It must be running. Changing it takes a new " +
					"backup.",
				Required:      true,
				Validators:    databaseRefValidators(),
				PlanModifiers: replace,
			},
			"phase": schema.StringAttribute{
				Description: backupPhaseDescription,
				Computed:    true,
			},
			"completed_at": schema.StringAttribute{
				Description: backupCompletedAtDescription,
				Computed:    true,
			},
			"size_bytes": schema.Int64Attribute{
				Description: backupSizeDescription,
				Computed:    true,
			},
			"source_version": schema.StringAttribute{
				Description: backupSourceVersionDescription,
				Computed:    true,
			},
			"source_storage": schema.StringAttribute{
				Description: backupSourceStorageDescription,
				Computed:    true,
			},
			"database_deleted": schema.BoolAttribute{
				Description: backupDatabaseDeletedDescription,
				Computed:    true,
			},
			"created_at": createdAtAttribute("the backup"),
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Delete: true,
			}),
		},
	}
}

// backupNameValidators check a backup name the way kupe-api does: a DNS-1123
// label of at most 63 characters.
func backupNameValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtMost(maxNameLength),
		stringvalidator.RegexMatches(namespacePattern,
			"must be lowercase letters, digits and '-', and start and end with a letter or digit"),
	}
}

func (r *DatabaseBackupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DatabaseBackupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := ""
	if !plan.Name.IsUnknown() {
		name = plan.Name.ValueString()
	}
	backup, etag, err := r.client.CreateDatabaseBackup(ctx, plan.Database.ValueString(), name)
	if err != nil {
		resp.Diagnostics.AddError("failed to create database backup", backupCreateErrorDetail(err))
		return
	}
	r.created(ctx, backup, etag, &plan, &resp.State, &resp.Diagnostics)
}

// backupCreateErrorDetail is apiErrorDetail for a backup create. A 409
// terminating is about the database, which the shared hint does not say.
func backupCreateErrorDetail(err error) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.Code == client.CodeTerminating {
		return apiErrorSummary(apiErr) + "\n\nThe database is being deleted, so it cannot be backed up."
	}
	return apiErrorDetail(err)
}

// Update only records a changed `timeouts`: every other argument replaces
// the backup. The backup is refreshed.
func (r *DatabaseBackupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DatabaseBackupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.update(ctx, &plan, &state, "", nil, false, &resp.State, &resp.Diagnostics)
}

func databaseBackupModelFromAPI(b *client.Backup) databaseBackupModel {
	status := b.Status
	if status == nil {
		status = &client.BackupStatus{}
	}
	source := status.Source
	if source == nil {
		source = &client.BackupSource{}
	}
	return databaseBackupModel{
		Name:            types.StringValue(b.Name),
		Database:        types.StringValue(b.Database),
		Phase:           types.StringValue(status.Phase),
		CompletedAt:     types.StringValue(status.CompletedAt),
		SizeBytes:       types.Int64Value(status.SizeBytes),
		SourceVersion:   types.StringValue(source.Version),
		SourceStorage:   types.StringValue(source.Storage),
		DatabaseDeleted: types.BoolValue(status.DatabaseDeleted),
		CreatedAt:       types.StringValue(b.CreatedAt),
	}
}
