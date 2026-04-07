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
	_ resource.Resource                = &TenantMemberResource{}
	_ resource.ResourceWithImportState = &TenantMemberResource{}
)

type TenantMemberResource struct {
	client *client.Client
}

type TenantMemberResourceModel struct {
	Email types.String `tfsdk:"email"`
	Role  types.String `tfsdk:"role"`
}

func NewTenantMemberResource() resource.Resource {
	return &TenantMemberResource{}
}

func (r *TenantMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant_member"
}

func (r *TenantMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages tenant membership in Kupe Cloud.",
		Attributes: map[string]schema.Attribute{
			"email": schema.StringAttribute{
				Description: "Member email address (immutable, used as identifier).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"role": schema.StringAttribute{
				Description: "Member role. Valid values are admin and readonly.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("admin", "readonly"),
				},
			},
		},
	}
}

func (r *TenantMemberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *TenantMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan TenantMemberResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	member, err := r.client.AddMember(ctx, client.AddMemberRequest{
		Email: plan.Email.ValueString(),
		Role:  plan.Role.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to add member", err.Error())
		return
	}

	plan.Email = types.StringValue(member.Email)
	plan.Role = types.StringValue(member.Role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TenantMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state TenantMemberResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	members, err := r.client.ListMembers(ctx)
	if err != nil {
		resp.Diagnostics.AddError("failed to list members", err.Error())
		return
	}

	email := state.Email.ValueString()
	for _, m := range members {
		if m.Email == email {
			state.Role = types.StringValue(m.Role)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}

	// Member not found — removed externally
	resp.State.RemoveResource(ctx)
}

func (r *TenantMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan TenantMemberResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	member, err := r.client.UpdateMember(ctx, plan.Email.ValueString(), client.UpdateMemberRequest{
		Role: plan.Role.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to update member", err.Error())
		return
	}

	plan.Role = types.StringValue(member.Role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TenantMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state TenantMemberResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.RemoveMember(ctx, state.Email.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("failed to remove member", err.Error())
	}
}

func (r *TenantMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("email"), req, resp)
}
