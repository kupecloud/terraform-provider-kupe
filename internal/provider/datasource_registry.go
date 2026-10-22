package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ datasource.DataSource = &RegistryDataSource{}

// RegistryDataSource is data.kupe_registry: the tenant's one registry.
type RegistryDataSource struct {
	client *client.Client
}

func NewRegistryDataSource() datasource.DataSource {
	return &RegistryDataSource{}
}

func (d *RegistryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry"
}

func (d *RegistryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the tenant's Kupe container registry. A tenant has at most one, named `registry`; " +
			"reading fails when the tenant has none.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Registry name: `registry`, the default and the only value accepted.",
				Optional:    true,
				Computed:    true,
				Validators:  registryNameValidators(),
			},
			"storage_limit": schema.StringAttribute{
				Description: "The tenant's own storage limit, e.g. `10Gi`. Null when the plan's limit applies.",
				Computed:    true,
			},
			"deletion_protection": schema.BoolAttribute{
				Description: "Whether the Kupe platform refuses to delete the registry.",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "Image path prefix to push to and pull from, e.g. `registry.kupe.cloud/acme`.",
				Computed:    true,
			},
			"storage_used_bytes": schema.Int64Attribute{
				Description: "What the registry stores, in bytes, as last measured by the platform.",
				Computed:    true,
			},
			"storage_limit_bytes": schema.Int64Attribute{
				Description: "The storage limit in force, in bytes. `-1` means unlimited. Null until the platform " +
					"has applied a limit.",
				Computed: true,
			},
			"repository_count": schema.Int64Attribute{
				Description: "Number of repositories in the registry.",
				Computed:    true,
			},
			"phase": schema.StringAttribute{
				Description: "Current phase: Pending, Provisioning, Running, Degraded or Terminating.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the registry was created.",
				Computed:    true,
			},
		},
	}
}

func (d *RegistryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *RegistryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config registryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := registryName
	if !config.Name.IsNull() && !config.Name.IsUnknown() {
		name = config.Name.ValueString()
	}

	reg, _, err := d.client.GetRegistry(ctx, name)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("no registry",
			"This tenant has no registry. Create it with the `kupe_registry` resource, or in the Kupe Console.")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("failed to read registry", apiErrorDetail(err))
		return
	}
	state := registryModelFromAPI(reg)
	state.Name = types.StringValue(name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
