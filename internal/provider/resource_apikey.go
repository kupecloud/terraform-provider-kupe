package provider

import (
	"context"
	"fmt"

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
		Description: "Manages a Kupe Cloud API key for machine-to-machine authentication.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "API key ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"display_name": schema.StringAttribute{
				Description: "Human-readable name for the API key (immutable).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"role": schema.StringAttribute{
				Description: "API key role. Valid values are admin and readonly. Immutable after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("admin", "readonly"),
				},
			},
			"expires_at": schema.StringAttribute{
				Description: "Expiration time in RFC3339 format. Optional and immutable after creation.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"key": schema.StringAttribute{
				Description: "The raw API key. Only available on creation and stored in Terraform state as sensitive. Sensitive redacts UI output but does not prevent the value from being written to state.",
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_by": schema.StringAttribute{
				Description: "Email of the user who created the key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the key was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *APIKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
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
		resp.Diagnostics.AddError("failed to create api key", err.Error())
		return
	}

	plan.ID = types.StringValue(apiKey.ID)
	plan.Key = types.StringValue(apiKey.Key)
	plan.CreatedBy = types.StringValue(apiKey.CreatedBy)
	plan.CreatedAt = types.StringValue(apiKey.CreatedAt)
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
		resp.Diagnostics.AddError("failed to list api keys", err.Error())
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
		if k.ExpiresAt != "" {
			state.ExpiresAt = types.StringValue(k.ExpiresAt)
		}
		// Key is only available on creation — preserve from state
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	// Key not found — deleted externally
	resp.State.RemoveResource(ctx)
}

func (r *APIKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// API keys are immutable — all fields use RequiresReplace
	resp.Diagnostics.AddError("api keys are immutable", "API keys cannot be updated, only replaced")
}

func (r *APIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state APIKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteAPIKey(ctx, state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to delete api key", err.Error())
	}
}

func (r *APIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by ID (e.g., "ak-abc123"). The raw key is not recoverable after import.
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
