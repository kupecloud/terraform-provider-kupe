package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Access token phases Create waits on. The API returns at once and the
// platform issues the token within seconds: until it is Ready the registry
// refuses it. Failed means it can never work.
const (
	registryTokenPhaseReady  = "Ready"
	registryTokenPhaseFailed = "Failed"
)

// registryTokenKind is the kind of an access token; the other kind, a login
// session, belongs to a person signed in with kupe and is never managed here.
const registryTokenKind = "token"

// defaultRegistryTokenCreateTimeout bounds waiting for a new token to be
// issued. The platform waits up to 2 minutes for the token's secret to reach
// it before failing the token.
const defaultRegistryTokenCreateTimeout = 5 * time.Minute

// registryTokenSecretDescription is the `secret` attribute's description,
// stated verbatim in the docs and the provider index (review M-9).
const registryTokenSecretDescription = "The token's secret, returned only when the token is created. It is " +
	"stored in Terraform state in plain text: Sensitive hides it from plan and apply output but does not keep it " +
	"out of the state file. Keep state in an encrypted backend with restricted access, or create tokens outside " +
	"Terraform."

// validRegistryTokenID is a token id: rt- and 16 lower-case hex characters.
var validRegistryTokenID = regexp.MustCompile(`^rt-[a-f0-9]{16}$`)

// registryTokenNow is the clock the expiry warning reads; tests set it.
var registryTokenNow = time.Now

var (
	_ resource.Resource                = &RegistryTokenResource{}
	_ resource.ResourceWithConfigure   = &RegistryTokenResource{}
	_ resource.ResourceWithImportState = &RegistryTokenResource{}
)

// RegistryTokenResource is kupe_registry_token. A token never changes, so it
// follows the kupe_api_key pattern rather than managedResource: every input
// requires replacement, and the secret exists only in the create response.
type RegistryTokenResource struct {
	client *client.Client
}

type RegistryTokenResourceModel struct {
	ID            types.String   `tfsdk:"id"`
	Registry      types.String   `tfsdk:"registry"`
	DisplayName   types.String   `tfsdk:"display_name"`
	Access        types.String   `tfsdk:"access"`
	ExpiresInDays types.Int64    `tfsdk:"expires_in_days"`
	Username      types.String   `tfsdk:"username"`
	RegistryHost  types.String   `tfsdk:"registry_host"`
	ExpiresAt     types.String   `tfsdk:"expires_at"`
	CreatedBy     types.String   `tfsdk:"created_by"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	LastUsedAt    types.String   `tfsdk:"last_used_at"`
	Phase         types.String   `tfsdk:"phase"`
	Secret        types.String   `tfsdk:"secret"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func NewRegistryTokenResource() resource.Resource {
	return &RegistryTokenResource{}
}

func (r *RegistryTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_token"
}

func (r *RegistryTokenResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manages an access token for the tenant's Kupe container registry: a registry username and " +
			"secret for CI and anything else that pulls or pushes images without a person signed in. Sign in with " +
			"`docker login <registry_host> --username <username> --password-stdin`. Create waits until the platform " +
			"has issued the token (seconds); until then the registry refuses it.\n\n" +
			"A token never changes: changing any argument replaces it with a new token and a new secret. It stops " +
			"working when it expires, when it is revoked, and when the member who created it leaves the tenant (or, " +
			"for a push token, loses the admin role); once the platform has removed it, the next plan creates a new " +
			"one. A registry holds at most 50 access tokens.\n\n" +
			"### Secret in state\n\n" + registryTokenSecretDescription,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Token ID: `rt-` and 16 hex characters.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"registry": schema.StringAttribute{
				Description: "Registry the token signs in to. A tenant has one registry, named `registry`: this is " +
					"the default and the only value accepted.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(registryName),
				Validators:    registryNameValidators(),
				PlanModifiers: replace,
			},
			"display_name": schema.StringAttribute{
				Description:   "A name to recognise the token by, 1 to 128 characters. A different name replaces the token.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.UTF8LengthBetween(1, 128)},
				PlanModifiers: replace,
			},
			"access": schema.StringAttribute{
				Description: "`pull`, or `push` (which includes pull). Only a member with the " +
					"`admin` role, or an admin API key, can create a push token. A different value replaces the token.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.OneOf("pull", "push")},
				PlanModifiers: replace,
			},
			"expires_in_days": schema.Int64Attribute{
				Description: "Days until the token stops working, 1 to 365. When unset, the platform's default " +
					"(90 days) is used and recorded here. A different value replaces the token.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{int64validator.Between(1, 365)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplace(),
				},
			},
			"username": schema.StringAttribute{
				Description:   "Registry username to sign in with, e.g. `kupe+acme+tok-0a1b2c3d4e5f6a7b`.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"registry_host": schema.StringAttribute{
				Description: "Registry host to sign in to, e.g. `registry.kupe.cloud`. Returned when the token is " +
					"created, so it is null after import.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"expires_at": schema.StringAttribute{
				Description:   "When the token stops working (RFC 3339).",
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_by": schema.StringAttribute{
				Description:   "Email of the member whose credentials created the token.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_at": createdAtAttribute("the token"),
			"last_used_at": schema.StringAttribute{
				Description: "When the token last pulled or pushed (RFC 3339), read hourly at most. Null until it " +
					"is first used.",
				Computed: true,
			},
			"phase": schema.StringAttribute{
				Description: "Pending until the platform has issued the token, then Ready. Failed means it can " +
					"never work: replace it.",
				Computed: true,
			},
			"secret": schema.StringAttribute{
				Description:   registryTokenSecretDescription + " Null after import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *RegistryTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (r *RegistryTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RegistryTokenResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	registry := plan.Registry.ValueString()
	createReq := client.CreateRegistryTokenRequest{
		DisplayName: plan.DisplayName.ValueString(),
		Access:      plan.Access.ValueString(),
	}
	if !plan.ExpiresInDays.IsNull() && !plan.ExpiresInDays.IsUnknown() {
		days := plan.ExpiresInDays.ValueInt64()
		createReq.ExpiresInDays = &days
	}
	created, err := r.client.CreateRegistryToken(ctx, registry, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create access token", registryChildCreateErrorDetail(err))
		return
	}
	// Ids only: the secret is never logged.
	tflog.Debug(ctx, "created registry access token", map[string]any{"id": created.ID, "registry": registry})

	plan.ID = types.StringValue(created.ID)
	plan.Secret = types.StringValue(created.Secret)
	plan.RegistryHost = types.StringValue(created.Registry)
	// The username in the response is the one the token is issued under;
	// Read takes the API's status.username once the platform has set it.
	plan.Username = types.StringValue(created.Username)
	plan.CreatedBy = types.StringValue(created.CreatedBy)
	plan.CreatedAt = types.StringValue(created.CreatedAt)
	plan.ExpiresAt = types.StringValue(created.ExpiresAt)
	plan.LastUsedAt = stringOrNull(created.LastUsedAt)
	plan.Phase = types.StringValue(created.Status.Phase)
	if plan.ExpiresInDays.IsUnknown() {
		plan.ExpiresInDays = tokenLifetimeDays(created.CreatedAt, created.ExpiresAt)
	}
	// Record the token, secret included, before waiting, so an interrupted
	// apply still tracks it.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultRegistryTokenCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitForIssued(ctx, timeout, &plan, &resp.State, &resp.Diagnostics)
}

// errTokenFailed ends waitForIssued when the token failed.
var errTokenFailed = errors.New("access token failed")

// waitForIssued polls the new token until the platform has issued it,
// recording each observation. Failed, a terminal API error and the timeout
// are errors: Terraform keeps the token in state, marked tainted, and the next
// apply replaces it.
func (r *RegistryTokenResource) waitForIssued(ctx context.Context, timeout time.Duration, m *RegistryTokenResourceModel, state *tfsdk.State, diags *diag.Diagnostics) {
	var last *client.RegistryToken
	err := pollUntil(ctx, timeout, func(c context.Context) (bool, error) {
		token, err := r.client.GetRegistryToken(c, m.Registry.ValueString(), m.ID.ValueString())
		if err != nil {
			return false, err
		}
		last = token
		mapRegistryTokenToState(token, m)
		diags.Append(state.Set(c, m)...)
		switch token.Status.Phase {
		case registryTokenPhaseReady:
			return true, nil
		case registryTokenPhaseFailed:
			return true, errTokenFailed
		}
		return false, nil
	})
	const tainted = "\n\nTerraform keeps the token in state, marked tainted, so the next `terraform apply` replaces it."
	switch {
	case err == nil:
	case errors.Is(err, errTokenFailed):
		detail := fmt.Sprintf("Access token %q could not be issued, and it can never work.", m.ID.ValueString())
		if last != nil && last.Status.Message != "" {
			detail += "\n\nStatus message: " + last.Status.Message
		}
		diags.AddError("access token failed", detail+tainted)
	case isTerminalAPIError(err):
		diags.AddError("access token create failed while waiting for Ready", apiErrorDetail(err)+tainted)
	default:
		phase := m.Phase.ValueString()
		if phase == "" {
			phase = "Pending"
		}
		diags.AddError("access token create timed out while "+phase,
			fmt.Sprintf("Access token %q was not issued before the create timeout fired; until it is, the registry "+
				"refuses it. To wait longer, set `timeouts.create = %q`.%s",
				m.ID.ValueString(), longerTimeout(timeout, defaultRegistryTokenCreateTimeout), tainted))
	}
}

func (r *RegistryTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RegistryTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	token, err := r.client.GetRegistryToken(ctx, state.Registry.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		// Revoked, removed after it expired, or its creator left the tenant:
		// the next plan creates a new token.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("failed to read access token", apiErrorDetail(err))
		return
	}
	if token.Kind != "" && token.Kind != registryTokenKind {
		resp.Diagnostics.AddError("not an access token",
			fmt.Sprintf("%q is a login session, which belongs to a person signed in with kupe; Terraform manages "+
				"access tokens only.", state.ID.ValueString()))
		return
	}

	state.DisplayName = types.StringValue(token.DisplayName)
	state.Access = types.StringValue(token.Access)
	state.CreatedBy = types.StringValue(token.CreatedBy)
	state.CreatedAt = types.StringValue(token.CreatedAt)
	state.ExpiresAt = types.StringValue(token.ExpiresAt)
	if state.ExpiresInDays.IsNull() {
		// Imported: the API returns the expiry, not the lifetime asked for.
		state.ExpiresInDays = tokenLifetimeDays(token.CreatedAt, token.ExpiresAt)
	}
	mapRegistryTokenToState(token, &state)
	// The secret and the registry host are only in the create response:
	// kept from state.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if expires, err := time.Parse(time.RFC3339, token.ExpiresAt); err == nil && !expires.After(registryTokenNow()) {
		resp.Diagnostics.AddWarning("access token expired",
			fmt.Sprintf("Access token %q expired at %s; it will be replaced on the next apply after the platform "+
				"removes it.", state.ID.ValueString(), token.ExpiresAt))
	}
}

// mapRegistryTokenToState records what GET reports that changes after
// create: the username once issued, when the token was last used and its
// phase. A username not yet set keeps the one in state.
func mapRegistryTokenToState(token *client.RegistryToken, m *RegistryTokenResourceModel) {
	if token.Username != "" {
		m.Username = types.StringValue(token.Username)
	} else if m.Username.IsUnknown() {
		m.Username = types.StringNull()
	}
	m.LastUsedAt = stringOrNull(token.LastUsedAt)
	m.Phase = types.StringValue(token.Status.Phase)
}

// tokenLifetimeDays is the whole number of days from createdAt to
// expiresAt, null when either is not RFC 3339. The platform computes the
// expiry from its own clock, a moment before it stamps the creation time, so
// the difference is rounded.
func tokenLifetimeDays(createdAt, expiresAt string) types.Int64 {
	created, err1 := time.Parse(time.RFC3339, createdAt)
	expires, err2 := time.Parse(time.RFC3339, expiresAt)
	if err1 != nil || err2 != nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(math.Round(expires.Sub(created).Hours() / 24)))
}

// Update records a change to `timeouts`, the only thing that can change in
// place: every argument requires replacement.
func (r *RegistryTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RegistryTokenResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RegistryTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RegistryTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteRegistryToken(ctx, state.Registry.ValueString(), state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to revoke access token", apiErrorDetail(err))
	}
}

// ImportState accepts "registry/<id>". The secret is not recoverable: it is
// null after import.
func (r *RegistryTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	registry, id, ok := parseDatabaseBindingImportID(req.ID)
	if !ok || registry != registryName || !validRegistryTokenID.MatchString(id) {
		resp.Diagnostics.AddError("invalid import ID",
			fmt.Sprintf("expected \"registry/<id>\", where the id is rt- and 16 hex characters, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("registry"), registry)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
