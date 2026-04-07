// Package provider implements the Terraform provider for kupe.
package provider

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var _ provider.Provider = &KupeProvider{}

// KupeProvider defines the kupe Terraform provider.
type KupeProvider struct {
	version string
}

// KupeProviderModel maps the provider schema to Go types.
type KupeProviderModel struct {
	Host   types.String `tfsdk:"host"`
	Tenant types.String `tfsdk:"tenant"`
	APIKey types.String `tfsdk:"api_key"`
	Token  types.String `tfsdk:"token"`
}

// New creates a new provider instance.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &KupeProvider{version: version}
	}
}

func (p *KupeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "kupe"
	resp.Version = p.version
}

func (p *KupeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Kupe Cloud tenant resources including clusters, secrets, members, and API keys.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Description: "Kupe API base URL, for example https://api.kupe.cloud. Use HTTPS for normal environments. HTTP is only allowed for local development hosts such as localhost. Can also be set with KUPE_HOST.",
				Optional:    true,
			},
			"tenant": schema.StringAttribute{
				Description: "Tenant name. Can also be set with KUPE_TENANT.",
				Optional:    true,
			},
			"api_key": schema.StringAttribute{
				Description: "API key for authentication. Mutually exclusive with token. Can also be set with KUPE_API_KEY.",
				Optional:    true,
				Sensitive:   true,
			},
			"token": schema.StringAttribute{
				Description: "OIDC bearer token for environments where API OIDC auth is explicitly enabled. Not supported on hosted Kupe Cloud. Mutually exclusive with api_key. Can also be set with KUPE_TOKEN.",
				Optional:    true,
				Sensitive:   true,
			},
		},
	}
}

func (p *KupeProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config KupeProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	host := stringValueOrEnv(config.Host, "KUPE_HOST")
	tenant := stringValueOrEnv(config.Tenant, "KUPE_TENANT")
	apiKey := stringValueOrEnv(config.APIKey, "KUPE_API_KEY")
	token := stringValueOrEnv(config.Token, "KUPE_TOKEN")

	if host == "" {
		resp.Diagnostics.AddError("missing host", "host must be set in the provider configuration or KUPE_HOST environment variable")
		return
	}
	if tenant == "" {
		resp.Diagnostics.AddError("missing tenant", "tenant must be set in the provider configuration or KUPE_TENANT environment variable")
		return
	}

	normalizedHost, err := normalizeHost(host)
	if err != nil {
		resp.Diagnostics.AddError("invalid host", err.Error())
		return
	}

	authToken, err := selectAuthToken(apiKey, token)
	if err != nil {
		resp.Diagnostics.AddError("invalid authentication", err.Error())
		return
	}

	c := client.New(normalizedHost, tenant, authToken)
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *KupeProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewClusterResource,
		NewSecretResource,
		NewTenantMemberResource,
		NewAPIKeyResource,
	}
}

func (p *KupeProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewTenantDataSource,
		NewClusterDataSource,
		NewPlanDataSource,
	}
}

// stringValueOrEnv returns the Terraform config value if set, otherwise the env var.
func stringValueOrEnv(v types.String, envKey string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(envKey)
}

func selectAuthToken(apiKey, token string) (string, error) {
	switch {
	case apiKey != "" && token != "":
		return "", fmt.Errorf("api_key and token are mutually exclusive; set only one authentication method")
	case apiKey != "":
		return apiKey, nil
	case token != "":
		return token, nil
	default:
		return "", fmt.Errorf("api_key or token must be set in the provider configuration, or via KUPE_API_KEY / KUPE_TOKEN environment variables")
	}
}

func normalizeHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("host must not be empty")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("host must be a valid URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("host must be an absolute URL including scheme and host, for example https://api.kupe.cloud")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("host must not include user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("host must not include query parameters or fragments")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("host must not include a path")
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("host must include a valid hostname")
	}

	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLocalDevelopmentHost(hostname) {
			return "", fmt.Errorf("host must use https unless you are connecting to a local development endpoint")
		}
	default:
		return "", fmt.Errorf("host must use https, or http for local development")
	}

	return parsed.Scheme + "://" + parsed.Host, nil
}

func isLocalDevelopmentHost(host string) bool {
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
