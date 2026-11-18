package provider

import (
	"context"
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ datasource.DataSource = &AppImageDataSource{}

// AppImageDataSource resolves a tag or digest during each data source read.
type AppImageDataSource struct {
	client *client.Client
}

type appImageModel struct {
	Image        types.String `tfsdk:"image"`
	Reference    types.String `tfsdk:"reference"`
	Platforms    types.List   `tfsdk:"platforms"`
	User         types.String `tfsdk:"user"`
	ExposedPorts types.List   `tfsdk:"exposed_ports"`
}

func NewAppImageDataSource() datasource.DataSource { return &AppImageDataSource{} }

func (d *AppImageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_image"
}

func (d *AppImageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Resolves an image tag or digest to an immutable sha256 reference through the Kupe API. " +
			"Use `reference` as `kupe_app_environment.runtime.image`. Every read resolves the image again, so a " +
			"moving tag can change the next plan and deploy on apply. The provider sends no registry credentials.",
		Attributes: map[string]schema.Attribute{
			"image": schema.StringAttribute{
				Description: "Image tag or digest, from your tenant's container registry or a public registry the platform allows.",
				Required:    true,
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 512)},
			},
			"reference": schema.StringAttribute{
				Description: "Immutable image reference with the registry host and `@sha256:` digest. For an image index, this is the index digest.",
				Computed:    true,
			},
			"platforms": schema.ListAttribute{
				Description: "Platforms the API reports, as `os/architecture` or `os/architecture/variant`. The list " +
					"can leave some out; a successful resolution confirms linux/amd64 support.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"user": schema.StringAttribute{
				Description: "Numeric non-root user the linux/amd64 image runs as, as checked by the platform.",
				Computed:    true,
			},
			"exposed_ports": schema.ListAttribute{
				Description: "TCP ports the linux/amd64 image exposes, as returned by the API.",
				Computed:    true,
				ElementType: types.Int64Type,
			},
		},
	}
}

func (d *AppImageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (d *AppImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state appImageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Image.IsUnknown() || state.Image.IsNull() {
		if state.Image.IsUnknown() && req.ClientCapabilities.DeferralAllowed {
			state.Reference = types.StringUnknown()
			state.Platforms = types.ListUnknown(types.StringType)
			state.User = types.StringUnknown()
			state.ExposedPorts = types.ListUnknown(types.Int64Type)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			if !resp.Diagnostics.HasError() {
				resp.Deferred = &datasource.Deferred{Reason: datasource.DeferredReasonDataSourceConfigUnknown}
			}
			return
		}
		resp.Diagnostics.AddAttributeError(path.Root("image"), "app image required",
			"Set `image` to an image tag or digest that is known when the data source is read.")
		return
	}
	image, err := d.client.ResolveAppImage(ctx, state.Image.ValueString())
	if err != nil {
		summary, detail := appImageErrorDiagnostic(err)
		resp.Diagnostics.AddAttributeError(path.Root("image"), summary, detail)
		return
	}
	if len(image.Reference) > 512 || !appDigestPattern.MatchString(image.Reference) {
		resp.Diagnostics.AddAttributeError(path.Root("image"), "invalid image resolution",
			"The Kupe API did not return an immutable sha256 image reference. Retry the plan, and contact Kupe Cloud support if it keeps happening.")
		return
	}
	platforms := make([]string, 0, len(image.Platforms))
	for _, p := range image.Platforms {
		name := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			name += "/" + p.Variant
		}
		platforms = append(platforms, name)
	}
	state.Reference = types.StringValue(image.Reference)
	state.User = types.StringValue(image.User)
	platformValues, platformDiags := types.ListValueFrom(ctx, types.StringType, platforms)
	ports := make([]int64, len(image.ExposedPorts))
	for i, port := range image.ExposedPorts {
		ports[i] = int64(port)
	}
	portValues, portDiags := types.ListValueFrom(ctx, types.Int64Type, ports)
	state.Platforms, state.ExposedPorts = platformValues, portValues
	resp.Diagnostics.Append(platformDiags...)
	resp.Diagnostics.Append(portDiags...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
}

// JSON API refusals carry the API's actionable message. Network failures and
// non-JSON proxy bodies can include private URLs, so keep those out of output.
func appImageErrorDiagnostic(err error) (string, string) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.NonJSON && apiErr.StatusCode == http.StatusNotFound {
		return "app image route not found", "The Kupe API returned 404 for the image lookup. Either the API is older than this provider and cannot resolve app images, or a proxy answered in its place. " +
			"Check the API version. To plan or destroy, replace `data.kupe_app_image` in your configuration with the image digest reference from Terraform state."
	}
	if !errors.As(err, &apiErr) || apiErr.NonJSON {
		return "failed to resolve app image", "The Kupe API could not resolve the image. Retry the plan, and contact Kupe Cloud support if it keeps happening."
	}

	summary := "failed to resolve app image"
	switch apiErr.Code {
	case client.CodeImageNotFound:
		summary = "app image not found"
	case client.CodeImageUnavailable:
		summary = "app image temporarily unavailable"
	case client.CodeImagePlatformUnsupported:
		summary = "app image platform unsupported"
	case client.CodeImagePolicyDenied:
		summary = "app image policy denied"
	case client.CodeAppsUnavailable:
		return "apps unavailable", apiErrorSummary(apiErr) + "\n\nApps are not enabled for this Kupe Cloud environment. " +
			"To plan or destroy, replace `data.kupe_app_image` in your configuration with the image digest reference from Terraform state."
	}
	return summary, apiErrorDetail(err)
}
