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
		Description: "Reads a Kupe database by name. Credentials are never returned.",
		Attributes: map[string]schema.Attribute{
			"postgres": schema.SingleNestedAttribute{Computed: true, Description: "The database's PostgreSQL databases, declared roles, extensions and connection pooler.", Attributes: map[string]schema.Attribute{
				"pooler": schema.SingleNestedAttribute{Computed: true, Description: poolerReadDescription, Attributes: map[string]schema.Attribute{
					"enabled":   schema.BoolAttribute{Computed: true, Description: "Whether the pooler is turned on."},
					"pool_mode": schema.StringAttribute{Computed: true, Description: "How server connections are shared: `transaction` or `session`."},
					"instances": schema.Int64Attribute{Computed: true, Description: poolerInstancesDescription},
				}},
				"databases": schema.ListNestedAttribute{Computed: true, Description: logicalDatabasesDescription, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":    schema.StringAttribute{Computed: true},
					"schemas": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{"name": schema.StringAttribute{Computed: true}}}},
				}}},
				"roles":      schema.SetNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{"name": schema.StringAttribute{Computed: true}}}},
				"extensions": schema.SetNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{"name": schema.StringAttribute{Computed: true}}}},
			}},
			"name": schema.StringAttribute{
				Description: "Database name to look up.",
				Required:    true,
			},
			"engine": schema.StringAttribute{
				Description: "Database engine, for example `postgres`.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "Engine major version you asked for, for example `18`.",
				Computed:    true,
			},
			"resources": schema.SingleNestedAttribute{
				Description: "Resources of each instance.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"cpu": schema.StringAttribute{
						Description: "CPU, for example `500m`.",
						Computed:    true,
					},
					"memory": schema.StringAttribute{
						Description: "Memory, for example `1Gi`.",
						Computed:    true,
					},
					"storage": schema.StringAttribute{
						Description: "Storage, for example `10Gi`.",
						Computed:    true,
					},
				},
			},
			"high_availability": schema.BoolAttribute{
				Description: "Whether the database runs two instances with a synchronous standby.",
				Computed:    true,
			},
			"deletion_protection": schema.BoolAttribute{
				Description: "Whether deletion protection is on, so the platform refuses to delete the database.",
				Computed:    true,
			},
			"backup": schema.SingleNestedAttribute{
				Description: "Backup settings. Null when they have never been set, in which case the defaults apply: " +
					"7 days and `Daily`.",
				Computed: true,
				Attributes: map[string]schema.Attribute{
					"retention": schema.Int64Attribute{
						Description: "How far back the backups reach, in days. Null when never set, in which case the " +
							"default of 7 applies.",
						Computed: true,
					},
					"schedule": schema.StringAttribute{
						Description: "How many full backups are kept within the retention period: `Daily` keeps " +
							"one for every day, and `DailyThenWeekly` one for each of the last 7 days and one a " +
							"week before that.",
						Computed: true,
					},
				},
			},
			"source": schema.SingleNestedAttribute{
				Description: "What the database was restored from. Null for a database that was not restored.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"backup": schema.StringAttribute{
						Description: "Name of the manual backup the database was restored from.",
						Computed:    true,
					},
					"database": schema.StringAttribute{
						Description: "Name of the database it was restored from.",
						Computed:    true,
					},
					"point_in_time": schema.StringAttribute{
						Description: "The point it was restored to, in RFC 3339 format. Null when it was restored to the " +
							"latest point or from a backup.",
						Computed: true,
					},
				},
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending, Provisioning, Running, Degraded or Terminating.",
				Computed:    true,
			},
			"current_version": schema.StringAttribute{
				Description: "Full engine version the database runs, for example `18.0`.",
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
