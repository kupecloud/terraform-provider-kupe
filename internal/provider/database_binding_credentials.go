package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var bindingCredentialsAttrTypes = map[string]attr.Type{"dedicated": types.BoolType, "rotation_trigger": types.StringType}

func bindingCredentialsValue(dedicated bool, trigger types.String) types.Object {
	return types.ObjectValueMust(bindingCredentialsAttrTypes, map[string]attr.Value{"dedicated": types.BoolValue(dedicated), "rotation_trigger": trigger})
}
func bindingDedicated(obj types.Object) bool {
	v, _ := obj.Attributes()["dedicated"].(types.Bool)
	return v.ValueBool()
}
func bindingTrigger(obj types.Object) types.String {
	if obj.IsUnknown() {
		return types.StringUnknown()
	}
	if obj.IsNull() {
		return types.StringNull()
	}
	v, _ := obj.Attributes()["rotation_trigger"].(types.String)
	return v
}
func bindingCredentialsSchema() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{Optional: true, Computed: true, Description: "Dedicated credentials give every namespace its own login and password. Passwords are never returned to Terraform.", Default: objectdefault.StaticValue(bindingCredentialsValue(false, types.StringNull())), Attributes: map[string]schema.Attribute{
		"dedicated": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Use a separate login and password for each namespace. Omitting it, or the whole `credentials` block, means false. False to true changes in place; true to false, including removing the block, replaces the binding. Owner bindings use shared credentials; custom bindings require dedicated credentials.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = req.StateValue.ValueBool() && !req.PlanValue.IsUnknown() && !req.PlanValue.ValueBool()
		}, "dedicated credentials cannot be turned off once enabled; the binding is replaced", "dedicated credentials cannot be turned off once enabled; the binding is replaced")}},
		"rotation_trigger": schema.StringAttribute{Optional: true, Description: "Set or change this value to rotate dedicated credentials on update. Removing it does not rotate. This is an arbitrary trigger value, not a password. Terraform keeps the value in state. Restart applications after delivery."},
	}}
}

var _ resource.ResourceWithValidateConfig = (*DatabaseBindingResource)(nil)

func (r *DatabaseBindingResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var role, roleName types.String
	var credentials types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("role"), &role)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("role_name"), &roleName)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("credentials"), &credentials)...)
	if resp.Diagnostics.HasError() || role.IsUnknown() {
		return
	}
	if role.ValueString() == "custom" {
		if roleName.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("role_name"), "Invalid binding credentials", "custom bindings require roleName: a declared role name, at most 63 characters")
		}
	} else if isSet(roleName) {
		resp.Diagnostics.AddAttributeError(path.Root("role_name"), "Invalid binding credentials", "roleName is only allowed when role is custom")
	}
	if credentials.IsUnknown() {
		return
	}
	var dedicated types.Bool
	if credentials.IsNull() {
		dedicated = types.BoolValue(false)
	} else {
		dedicated, _ = credentials.Attributes()["dedicated"].(types.Bool)
		if dedicated.IsNull() {
			dedicated = types.BoolValue(false)
		}
	}
	if dedicated.IsUnknown() {
		return
	}
	if role.ValueString() == "owner" && dedicated.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("credentials"), "Invalid binding credentials", "owner bindings use shared credentials")
	}
	if role.ValueString() == "custom" && !dedicated.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("credentials"), "Invalid binding credentials", "custom bindings require credentials.dedicated to be true")
	}
	if isSet(bindingTrigger(credentials)) && !dedicated.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("credentials").AtName("rotation_trigger"), "Credentials are shared", "rotation_trigger requires credentials.dedicated to be true")
	}
}

var bindingRoleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// rotateCredentials issues exactly one POST, then follows the latest opaque token.
// Acknowledgement or an attempted reconcile is insufficient: all namespace
// delivery tokens and the delivered generation must agree before completion.
func (r *DatabaseBindingResource) rotateCredentials(ctx context.Context, model *DatabaseBindingResourceModel, trigger types.String, resp *resource.UpdateResponse) {
	result, err := r.client.RotateDatabaseBindingCredentials(ctx, model.Database.ValueString(), model.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to rotate database binding credentials", apiErrorDetail(err))
		return
	}
	if result.RequestedAt == "" {
		resp.Diagnostics.AddError("failed to rotate database binding credentials", "The API returned no rotation request token. Refresh the binding before requesting another rotation.")
		return
	}
	model.Credentials = bindingCredentialsValue(bindingDedicated(model.Credentials), trigger)
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	target := result.RequestedAt
	err = waitForCondition(ctx, func(ctx context.Context) (bool, error) {
		b, etag, err := r.client.GetDatabaseBinding(ctx, model.Database.ValueString(), model.Name.ValueString())
		if err != nil {
			if isTerminalAPIError(err) {
				return true, fmt.Errorf("read binding rotation: %w", err)
			}
			return false, nil
		}
		mapDatabaseBindingToState(b, etag, model)
		resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
		if resp.Diagnostics.HasError() {
			return true, fmt.Errorf("record binding rotation state")
		}
		if databaseBindingFailed(b) {
			return true, fmt.Errorf("%s", databaseBindingFailedDetail("update", model, b))
		}
		if b.Status == nil || b.Status.Rotation == nil {
			return false, nil
		}
		rotation := b.Status.Rotation
		if rotation.RequestedAt != "" {
			target = rotation.RequestedAt
		}
		if !databaseBindingRotationDelivered(b, target) {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("failed to wait for database binding rotation", apiErrorDetail(err))
	}
}

func databaseBindingRotationDelivered(b *client.DatabaseBinding, target string) bool {
	if target == "" || !databaseBindingDelivered(b) || b.Status.Rotation == nil {
		return false
	}
	rotation := b.Status.Rotation
	if rotation.RequestedAt != target || rotation.Pending || rotation.AppliedAt != target || b.Status.ObservedRotation != target {
		return false
	}
	for _, ns := range b.Status.Namespaces {
		if ns.DeliveredRotation != target {
			return false
		}
	}
	return true
}
