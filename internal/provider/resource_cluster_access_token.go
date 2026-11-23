package provider

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Cluster access token phases Create waits on. The API returns at once and
// the platform issues the token within seconds; until it is Ready it cannot
// be collected. Failed means it can never work.
const (
	clusterAccessTokenPhaseReady  = "Ready"
	clusterAccessTokenPhaseFailed = "Failed"
)

// clusterAccessRoleAdmin is the one access.role v1 offers.
const clusterAccessRoleAdmin = "admin"

// defaultClusterAccessTokenCreateTimeout bounds waiting for a new token to be
// issued and collected: the same bound as the CLI (CAT decision 5).
const defaultClusterAccessTokenCreateTimeout = 2 * time.Minute

// clusterAccessTokenSecretDescription is the `token` and `kubeconfig`
// attributes' description, stated verbatim in the docs and the provider
// index.
const clusterAccessTokenSecretDescription = "Returned only once, when Terraform collects the token during create. " +
	"It is marked sensitive, which hides it in plan and apply output, but it is stored in Terraform state in plain " +
	"text, and it gives `admin` access to the cluster. Keep state in an encrypted backend with restricted access, " +
	"or create tokens outside Terraform."

// validClusterAccessTokenID is a token id: cat- and 16 lower-case hex
// characters.
var validClusterAccessTokenID = regexp.MustCompile(`^cat-[a-f0-9]{16}$`)

// clusterAccessTokenNow is the clock the expiry warning reads; tests set it.
var clusterAccessTokenNow = time.Now

var (
	_ resource.Resource                = &ClusterAccessTokenResource{}
	_ resource.ResourceWithConfigure   = &ClusterAccessTokenResource{}
	_ resource.ResourceWithImportState = &ClusterAccessTokenResource{}
)

// ClusterAccessTokenResource is kupe_cluster_access_token. It mirrors
// kupe_registry_token: every input requires replacement, and the token and
// kubeconfig exist only in the one collect, which Create makes once the token
// is Ready.
type ClusterAccessTokenResource struct {
	client *client.Client
}

type ClusterAccessTokenResourceModel struct {
	ID            types.String              `tfsdk:"id"`
	Cluster       types.String              `tfsdk:"cluster"`
	DisplayName   types.String              `tfsdk:"display_name"`
	Access        *ClusterAccessTokenAccess `tfsdk:"access"`
	ExpiresInDays types.Int64               `tfsdk:"expires_in_days"`
	ExpiresAt     types.String              `tfsdk:"expires_at"`
	CreatedBy     types.String              `tfsdk:"created_by"`
	CreatedAt     types.String              `tfsdk:"created_at"`
	Phase         types.String              `tfsdk:"phase"`
	Collected     types.Bool                `tfsdk:"collected"`
	Token         types.String              `tfsdk:"token"`
	Kubeconfig    types.String              `tfsdk:"kubeconfig"`
	Timeouts      timeouts.Value            `tfsdk:"timeouts"`
}

// ClusterAccessTokenAccess is the `access` block.
type ClusterAccessTokenAccess struct {
	Role types.String `tfsdk:"role"`
}

func NewClusterAccessTokenResource() resource.Resource {
	return &ClusterAccessTokenResource{}
}

func (r *ClusterAccessTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_access_token"
}

func (r *ClusterAccessTokenResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manages an access token for a Kupe cluster: a Kubernetes token and a ready-to-use kubeconfig " +
			"for CI, and anything else that runs `kubectl` or a Kubernetes client without a person signed in. " +
			"Create waits until the platform has issued the token, then collects the token and kubeconfig once; " +
			"the platform never shows them again.\n\n" +
			"A token never changes in place: changing any argument replaces it with a new token. It stops working " +
			"when it expires, when it is revoked and when its cluster is deleted; once the platform has removed it, " +
			"the next plan creates a new one. Removing the member who created it does not revoke it. Only a member " +
			"with the `admin` role, or an admin API key, can create one. A cluster holds at most 20 access tokens.\n\n" +
			"### Token in state\n\n" + clusterAccessTokenSecretDescription,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Token ID: `cat-` and 16 hex characters.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"cluster": schema.StringAttribute{
				Description:   "Name of the cluster the token gives access to. A different cluster replaces the token.",
				Required:      true,
				Validators:    clusterRefValidators(),
				PlanModifiers: replace,
			},
			"display_name": schema.StringAttribute{
				Description: "A name to recognise the token by, 1 to 128 characters. A different name replaces the token.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 128),
					stringvalidator.RegexMatches(printableDisplayName,
						"must be printable text, without control characters"),
				},
				PlanModifiers: replace,
			},
			"access": schema.SingleNestedAttribute{
				Description:   "What the token may do in the cluster. A different value replaces the token.",
				Required:      true,
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"role": schema.StringAttribute{
						Description:   "`admin`: full access to the cluster, as its `cluster-admin`.",
						Required:      true,
						Validators:    []validator.String{stringvalidator.OneOf(clusterAccessRoleAdmin)},
						PlanModifiers: replace,
					},
				},
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
			"expires_at": schema.StringAttribute{
				Description:   "When the token stops working, in RFC 3339 format.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_by": schema.StringAttribute{
				Description: "Email address of the member, or `ak-` and the ID of the API key, whose credentials " +
					"created the token.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_at": createdAtAttribute("the token"),
			"phase": schema.StringAttribute{
				Description: "`Pending` until the platform has issued the token, then `Ready`. `Failed` means it can " +
					"never work: replace it. `Revoking` while it is being revoked.",
				Computed: true,
			},
			"collected": schema.BoolAttribute{
				Description: "Whether the token has been collected. Terraform collects it during create; the " +
					"platform serves it only once.",
				Computed: true,
			},
			"token": schema.StringAttribute{
				Description:   "The bearer token. " + clusterAccessTokenSecretDescription + " Null after import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"kubeconfig": schema.StringAttribute{
				Description: "A kubeconfig for the cluster that signs in with the token: its endpoint, certificate " +
					"authority, user and context. " + clusterAccessTokenSecretDescription + " Null after import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *ClusterAccessTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (r *ClusterAccessTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ClusterAccessTokenResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster := plan.Cluster.ValueString()
	createReq := client.CreateClusterAccessTokenRequest{
		DisplayName: plan.DisplayName.ValueString(),
		Access:      client.ClusterAccess{Role: plan.Access.Role.ValueString()},
	}
	if !plan.ExpiresInDays.IsNull() && !plan.ExpiresInDays.IsUnknown() {
		days := plan.ExpiresInDays.ValueInt64()
		createReq.ExpiresInDays = &days
	}
	created, err := r.client.CreateClusterAccessToken(ctx, cluster, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create cluster access token", apiErrorDetail(err))
		return
	}
	// Ids only: the token is never logged.
	tflog.Debug(ctx, "created cluster access token", map[string]any{"id": created.ID, "cluster": cluster})

	plan.ID = types.StringValue(created.ID)
	plan.CreatedBy = types.StringValue(created.CreatedBy)
	plan.CreatedAt = types.StringValue(created.CreatedAt)
	plan.ExpiresAt = types.StringValue(created.ExpiresAt)
	plan.Phase = types.StringValue(created.Status.Phase)
	plan.Collected = types.BoolValue(created.Collected)
	plan.Token = types.StringNull()
	plan.Kubeconfig = types.StringNull()
	if plan.ExpiresInDays.IsUnknown() {
		plan.ExpiresInDays = tokenLifetimeDays(created.CreatedAt, created.ExpiresAt)
	}
	// Record the token before waiting, so an interrupted apply still tracks
	// it and a destroy revokes it.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultClusterAccessTokenCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.waitAndCollect(ctx, timeout, &plan, &resp.State, &resp.Diagnostics)
}

// Ends of waitAndCollect other than success.
var (
	errClusterAccessTokenFailed    = errors.New("cluster access token failed")
	errClusterAccessTokenGone      = errors.New("cluster access token removed")
	errClusterAccessTokenCollected = errors.New("cluster access token already collected")
	errClusterAccessTokenLost      = errors.New("cluster access token collected but not delivered")
)

// collectRefusal is a collect the API refused for good: the token failed,
// or another credential created it.
type collectRefusal struct{ err error }

func (e *collectRefusal) Error() string { return e.err.Error() }
func (e *collectRefusal) Unwrap() error { return e.err }

// waitAndCollect polls the new token until it is Ready, then collects it
// once, recording each observation. A collect the API is not ready for
// (409 ACCESS_TOKEN_NOT_READY, also after a claim conflict) is tried again
// within the same timeout. Failed, a refused collect, a terminal API error
// and the timeout are errors: Terraform keeps the token in state, marked
// tainted, and the next apply replaces it. A token that is gone (404) is
// removed from state, as there is nothing left to revoke.
func (r *ClusterAccessTokenResource) waitAndCollect(ctx context.Context, timeout time.Duration, m *ClusterAccessTokenResourceModel, state *tfsdk.State, diags *diag.Diagnostics) {
	cluster, id := m.Cluster.ValueString(), m.ID.ValueString()
	var last *client.ClusterAccessToken
	// collectSent is set once a collect was delivered (answered 2xx, or a
	// 5xx or transport error after sending), not when it was refused: a later "already
	// collected" may then be this apply's own collect whose answer was lost
	// (in transit, at the deadline, or a 500 after the platform's claim).
	collectSent := false
	err := pollUntil(ctx, timeout, func(c context.Context) (bool, error) {
		token, err := r.client.GetClusterAccessToken(c, cluster, id)
		if client.IsNotFound(err) {
			return true, errClusterAccessTokenGone
		}
		if err != nil {
			return false, err
		}
		last = token
		mapClusterAccessTokenToState(token, m)
		diags.Append(state.Set(c, m)...)
		switch token.Status.Phase {
		case clusterAccessTokenPhaseFailed:
			return true, errClusterAccessTokenFailed
		case clusterAccessTokenPhaseReady:
		default:
			return false, nil
		}
		if token.Collected {
			if collectSent {
				return true, errClusterAccessTokenLost
			}
			return true, errClusterAccessTokenCollected
		}
		sentBefore := collectSent
		done, delivered, err := r.collect(c, m, state, diags)
		if delivered {
			collectSent = true
		}
		if sentBefore && errors.Is(err, errClusterAccessTokenCollected) {
			return true, errClusterAccessTokenLost
		}
		return done, err
	})

	const tainted = "\n\nTerraform keeps the token in state, marked tainted, so the next `terraform apply` replaces it."
	var refusal *collectRefusal
	switch {
	case err == nil:
	case errors.Is(err, errClusterAccessTokenGone):
		state.RemoveResource(ctx)
		diags.AddError("cluster access token removed before it was collected",
			fmt.Sprintf("Access token %q was revoked, expired, or its cluster was deleted before Terraform could "+
				"collect it, so there is nothing to revoke. The next `terraform apply` creates a new one.", id))
	case errors.Is(err, errClusterAccessTokenFailed):
		detail := fmt.Sprintf("Access token %q could not be issued, and it can never work.", id)
		if last != nil && last.Status.Message != "" {
			detail += "\n\nStatus message: " + last.Status.Message
		}
		diags.AddError("cluster access token failed", detail+tainted)
	case errors.Is(err, errClusterAccessTokenLost):
		diags.AddError("cluster access token could not be delivered",
			fmt.Sprintf("The platform served access token %q to this apply, but its answer did not reach "+
				"Terraform, so the token is lost: it cannot be shown again. It is tainted, and the next "+
				"`terraform apply` revokes it and creates a new one.", id))
	case errors.Is(err, errClusterAccessTokenCollected):
		diags.AddError("cluster access token already collected",
			fmt.Sprintf("access token %q was collected outside this apply, so Terraform cannot hold it; it is "+
				"tainted and the next apply replaces it.", id))
	case errors.As(err, &refusal):
		diags.AddError("cluster access token could not be collected", apiErrorDetail(refusal.err)+tainted)
	case isTerminalAPIError(err):
		diags.AddError("cluster access token create failed while waiting for Ready", apiErrorDetail(err)+tainted)
	case ctx.Err() != nil:
		diags.AddError("cluster access token create interrupted",
			fmt.Sprintf("The apply stopped before access token %q was collected.%s", id, tainted))
	default:
		phase := m.Phase.ValueString()
		if phase == "" {
			phase = "Pending"
		}
		diags.AddError("cluster access token create timed out while "+phase,
			fmt.Sprintf("Access token %q was not issued and collected before the create timeout ran out. To wait "+
				"longer, set `timeouts.create = %q`.%s",
				id, longerTimeout(timeout, defaultClusterAccessTokenCreateTimeout), tainted))
	}
}

// collect makes the one collect of a Ready token and records the token and
// kubeconfig in state. It reports done=false to try again on 409
// ACCESS_TOKEN_NOT_READY and on any transient error. delivered is whether
// the request reached the API rather than being refused.
func (r *ClusterAccessTokenResource) collect(ctx context.Context, m *ClusterAccessTokenResourceModel, state *tfsdk.State, diags *diag.Diagnostics) (done, delivered bool, _ error) {
	collected, err := r.client.CollectClusterAccessToken(ctx, m.Cluster.ValueString(), m.ID.ValueString())
	delivered = client.RequestDelivered(err)
	done, err = r.collected(ctx, m, state, diags, collected, err)
	return done, delivered, err
}

func (r *ClusterAccessTokenResource) collected(ctx context.Context, m *ClusterAccessTokenResourceModel, state *tfsdk.State, diags *diag.Diagnostics, collected *client.CollectedClusterAccessToken, err error) (bool, error) {
	switch code := client.ErrorCode(err); {
	case err == nil:
	case client.IsNotFound(err):
		return true, errClusterAccessTokenGone
	case code == client.CodeAccessTokenNotReady:
		return false, nil
	case code == client.CodeAccessTokenAlreadyCollected:
		return true, errClusterAccessTokenCollected
	case code == client.CodeAccessTokenFailed, code == client.CodeAccessTokenCollectCreatorOnly:
		return true, &collectRefusal{err: err}
	default:
		// pollUntil ends on a terminal error and retries anything else,
		// such as 503 while the cluster has no endpoint yet.
		return false, err
	}
	// Ids only: the token and kubeconfig are never logged.
	tflog.Debug(ctx, "collected cluster access token", map[string]any{"id": m.ID.ValueString(), "cluster": m.Cluster.ValueString()})
	m.Token = types.StringValue(collected.Token)
	m.Kubeconfig = types.StringValue(collected.Kubeconfig)
	m.Collected = types.BoolValue(true)
	diags.Append(state.Set(ctx, m)...)
	return true, nil
}

func (r *ClusterAccessTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ClusterAccessTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	token, err := r.client.GetClusterAccessToken(ctx, state.Cluster.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		// Revoked, removed after it expired, or the cluster was deleted:
		// the next plan creates a new token.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("failed to read cluster access token", apiErrorDetail(err))
		return
	}

	state.DisplayName = types.StringValue(token.DisplayName)
	state.Access = &ClusterAccessTokenAccess{Role: types.StringValue(token.Access.Role)}
	state.CreatedBy = types.StringValue(token.CreatedBy)
	state.CreatedAt = types.StringValue(token.CreatedAt)
	if state.ExpiresInDays.IsNull() {
		// Imported: the API returns the expiry, not the lifetime asked for.
		state.ExpiresInDays = tokenLifetimeDays(token.CreatedAt, token.ExpiresAt)
	}
	mapClusterAccessTokenToState(token, &state)
	// The token and kubeconfig are only in the collect response: kept from
	// state, and collect is never called again.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if expires, err := time.Parse(time.RFC3339, token.ExpiresAt); err == nil && !expires.After(clusterAccessTokenNow()) {
		resp.Diagnostics.AddWarning("cluster access token expired",
			fmt.Sprintf("Access token %q expired at %s. Once the platform removes it, the next apply creates "+
				"a new one.", state.ID.ValueString(), token.ExpiresAt))
	}
}

// mapClusterAccessTokenToState records what GET reports that changes after
// create: the expiry, the phase and whether it was collected.
func mapClusterAccessTokenToState(token *client.ClusterAccessToken, m *ClusterAccessTokenResourceModel) {
	m.ExpiresAt = types.StringValue(token.ExpiresAt)
	m.Phase = types.StringValue(token.Status.Phase)
	m.Collected = types.BoolValue(token.Collected)
}

// Update records a change to `timeouts`, the only thing that can change in
// place: every argument requires replacement.
func (r *ClusterAccessTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ClusterAccessTokenResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ClusterAccessTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ClusterAccessTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteClusterAccessToken(ctx, state.Cluster.ValueString(), state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to revoke cluster access token", apiErrorDetail(err))
	}
}

// ImportState accepts "<cluster>/<id>". The token and kubeconfig are not
// recoverable: they are null after import.
func (r *ClusterAccessTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cluster, id, ok := parseDatabaseBindingImportID(req.ID)
	if !ok || len(cluster) > maxNameLength || !clusterRefPattern.MatchString(cluster) || !validClusterAccessTokenID.MatchString(id) {
		resp.Diagnostics.AddError("invalid import ID",
			fmt.Sprintf("expected \"<cluster>/<id>\", where the cluster is a cluster name and the id is cat- and "+
				"16 hex characters, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
