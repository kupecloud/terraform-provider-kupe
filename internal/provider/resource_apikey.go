package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var (
	_ resource.Resource                = &APIKeyResource{}
	_ resource.ResourceWithImportState = &APIKeyResource{}
)

type APIKeyResource struct {
	client *client.Client
}

type APIKeyResourceModel struct {
	ID          types.String `tfsdk:"id"`
	DisplayName types.String `tfsdk:"display_name"`
	Role        types.String `tfsdk:"role"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
	Key         types.String `tfsdk:"key"`
	CreatedBy   types.String `tfsdk:"created_by"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func NewAPIKeyResource() resource.Resource {
	return &APIKeyResource{}
}

func (r *APIKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *APIKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Kupe Cloud API key, for tools and pipelines that call the API. Every attribute is fixed at creation: changing one replaces the key.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "API key ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"display_name": schema.StringAttribute{
				Description: "Name for the API key. Changing it replaces the key.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"role": schema.StringAttribute{
				Description: "API key's role: `admin` or `readonly`. Changing it replaces the key.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("admin", "readonly"),
				},
			},
			"expires_at": schema.StringAttribute{
				Description: "When the key expires, in RFC 3339 format. Changing it replaces the key. If you " +
					"omit it, the platform decides whether the key expires, and the provider records the " +
					"result in state.",
				Optional: true,
				// Computed so a server-assigned expiry (when the user leaves
				// this null) is recorded in state without Terraform seeing a
				// null→non-null diff that would otherwise force a perpetual
				// destroy/recreate of the credential (RequiresReplace). The
				// value is still immutable post-create: a user-driven change
				// forces replacement, while UseStateForUnknown keeps the
				// server-resolved value stable across unrelated re-plans.
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"key": schema.StringAttribute{
				Description: "The API key itself. It is only returned when the key is created. It is marked sensitive, which hides it in plan and apply output, but it is stored in Terraform state in plain text.",
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_by": schema.StringAttribute{
				Description: "Email address of the user who created the key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": createdAtAttribute("the key"),
		},
	}
}

func (r *APIKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req.ProviderData, &resp.Diagnostics)
}

func (r *APIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan APIKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := client.CreateAPIKeyRequest{
		DisplayName: plan.DisplayName.ValueString(),
		Role:        plan.Role.ValueString(),
	}
	if !plan.ExpiresAt.IsNull() {
		createReq.ExpiresAt = plan.ExpiresAt.ValueString()
	}

	apiKey, err := r.client.CreateAPIKey(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("failed to create api key", apiErrorDetail(err))
		return
	}

	plan.ID = types.StringValue(apiKey.ID)
	plan.Key = types.StringValue(apiKey.Key)
	plan.CreatedBy = types.StringValue(apiKey.CreatedBy)
	plan.CreatedAt = types.StringValue(apiKey.CreatedAt)
	// expires_at is Computed: record the server-resolved value (which may
	// be empty if the key never expires) so state matches what the server
	// returned. Without this the framework would error on an unknown
	// Computed value left unset after apply.
	plan.ExpiresAt = stringOrNull(apiKey.ExpiresAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *APIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state APIKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// API keys can only be listed (no GET by ID endpoint that returns the key).
	// We list and find by ID to verify existence.
	keys, err := r.client.ListAPIKeys(ctx)
	if err != nil {
		resp.Diagnostics.AddError("failed to list api keys", apiErrorDetail(err))
		return
	}

	id := state.ID.ValueString()
	for _, k := range keys {
		if k.ID != id {
			continue
		}
		state.DisplayName = types.StringValue(k.DisplayName)
		state.Role = types.StringValue(k.Role)
		state.CreatedBy = types.StringValue(k.CreatedBy)
		state.CreatedAt = types.StringValue(k.CreatedAt)
		// Mirror the server's expiry exactly — including clearing to null
		// when the server reports none — so drift is detected honestly
		// rather than masked by a stale value. expires_at is Computed, so
		// writing null here is valid even when config omitted it.
		state.ExpiresAt = stringOrNull(k.ExpiresAt)
		// Key is only available on creation — preserve from state
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	// Key not found — deleted externally
	resp.State.RemoveResource(ctx)
}

func (r *APIKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// API keys are immutable — all fields use RequiresReplace
	resp.Diagnostics.AddError("api keys are immutable", "An API key cannot be changed in place. Changing any attribute replaces it.")
}

func (r *APIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state APIKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteAPIKey(ctx, state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to delete api key", apiErrorDetail(err))
	}
}

func (r *APIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by ID (e.g., "ak-abc123"). The raw key is not recoverable after import.
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
