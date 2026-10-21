package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ datasource.DataSource = &DatabaseDataSource{}

type DatabaseDataSource struct {
	client *client.Client
}

func NewDatabaseDataSource() datasource.DataSource {
	return &DatabaseDataSource{}
}

func (d *DatabaseDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (d *DatabaseDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing Kupe database by name. Credentials are never returned.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Database name to look up.",
				Required:    true,
			},
			"engine": schema.StringAttribute{
				Description: "Database engine, e.g. `postgres`.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "Requested engine major version, e.g. `18`.",
				Computed:    true,
			},
			"resources": schema.SingleNestedAttribute{
				Description: "Per-instance resources.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Description: "CPU, e.g. `500m`.",
						Computed:    true,
					},
					"memory": schema.StringAttribute{
						Description: "Memory, e.g. `1Gi`.",
						Computed:    true,
					},
					"storage": schema.StringAttribute{
						Description: "Storage, e.g. `10Gi`.",
						Computed:    true,
					},
				},
			},
			"high_availability": schema.BoolAttribute{
				Description: "Whether the database runs two instances with a synchronous standby.",
				Computed:    true,
			},
			"deletion_protection": schema.BoolAttribute{
				Description: "Whether the Kupe platform refuses to delete the database.",
				Computed:    true,
			},
			"backup": schema.SingleNestedAttribute{
				Description: "Backup settings. Null when the database has never set them: it keeps the defaults, " +
					"7 days and `Daily`.",
				Computed: true,
				Attributes: map[string]schema.Attribute{
					"retention": schema.Int64Attribute{
						Description: "How far back the backups reach, in days. Null when never set: the default, 7, " +
							"applies.",
						Computed: true,
					},
					"schedule": schema.StringAttribute{
						Description: "How many full backups are kept within the retention period: `Daily` keeps " +
							"one for every day, `DailyThenWeekly` one for each of the last 7 days and one a week " +
							"before that.",
						Computed: true,
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
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the database was created.",
				Computed:    true,
			},
		},
	}
}

func (d *DatabaseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *DatabaseDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config databaseModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	db, _, err := d.client.GetDatabase(ctx, config.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to read database", apiErrorDetail(err))
		return
	}
	state := databaseModelFromAPI(db)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
