package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ datasource.DataSource = &DatabaseBackupDataSource{}

type DatabaseBackupDataSource struct {
	client *client.Client
}

func NewDatabaseBackupDataSource() datasource.DataSource {
	return &DatabaseBackupDataSource{}
}

func (d *DatabaseBackupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_backup"
}

func (d *DatabaseBackupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a manual backup of a Kupe database by name, including the backups of databases " +
			"that were deleted.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Backup name to look up.",
				Required:    true,
				Validators:  backupNameValidators(),
			},
			"database": schema.StringAttribute{
				Description: "Name of the database the backup was taken of. It may have been deleted since.",
				Computed:    true,
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
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the backup was created.",
				Computed:    true,
			},
		},
	}
}

func (d *DatabaseBackupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *DatabaseBackupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config databaseBackupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	backup, _, err := d.client.GetBackup(ctx, config.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to read database backup", apiErrorDetail(err))
		return
	}
	state := databaseBackupModelFromAPI(backup)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
