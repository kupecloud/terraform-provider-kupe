package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// maxNameLength is the Kubernetes object name limit kupe-api enforces.
const maxNameLength = 63

// rejectInPlaceChange fails the plan when the value of an existing resource
// changes. It is for fields the API cannot change where replacing the
// resource would destroy data: the user should see an error and decide how
// to migrate, not a plan that silently deletes and recreates. summary says
// why; detail says what to do instead.
func rejectInPlaceChange(summary, detail string) planmodifier.String {
	return rejectInPlaceChangeModifier{summary: summary, detail: detail}
}

type rejectInPlaceChangeModifier struct {
	summary, detail string
}

func (m rejectInPlaceChangeModifier) Description(_ context.Context) string {
	return "Cannot be changed after creation."
}

func (m rejectInPlaceChangeModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m rejectInPlaceChangeModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.PlanValue.IsUnknown() || req.PlanValue.Equal(req.StateValue) {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, m.summary,
		fmt.Sprintf("%s is %q and cannot change to %q in place. %s",
			req.Path, req.StateValue.ValueString(), req.PlanValue.ValueString(), m.detail))
}

// storageNeverShrinks fails the plan when a storage quantity decreases.
// Volumes can only grow, so the API would refuse the change at apply time;
// failing the plan shows the problem before anything is applied. Values it
// cannot parse are left for the API to validate.
func storageNeverShrinks() planmodifier.String {
	return storageNeverShrinksModifier{}
}

type storageNeverShrinksModifier struct{}

func (m storageNeverShrinksModifier) Description(_ context.Context) string {
	return "Storage can only grow."
}

func (m storageNeverShrinksModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m storageNeverShrinksModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.IsNull() {
		return
	}
	planned, okPlanned := parseSizeQuantity(req.PlanValue.ValueString())
	current, okCurrent := parseSizeQuantity(req.StateValue.ValueString())
	if !okPlanned || !okCurrent || planned.Cmp(current) >= 0 {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path,
		"Storage cannot shrink",
		fmt.Sprintf("%s is %s and can only grow: %s is smaller. Keep the current size or set a larger one.",
			req.Path, req.StateValue.ValueString(), req.PlanValue.ValueString()))
}

// databaseBindingNameDefault plans the binding name when it is not
// configured. kupe-api requires a name and has no default, so the provider
// derives "{database}-{cluster}-{namespace}-{role}" from the fields that
// identify a binding (the API allows one per such tuple). An
// existing binding keeps the name it has, so importing one or removing
// `name` from configuration never forces a replacement.
func databaseBindingNameDefault() planmodifier.String {
	return databaseBindingNameDefaultModifier{}
}

type databaseBindingNameDefaultModifier struct{}

// databaseBindingNameParts are the attributes the default name joins, in order.
var databaseBindingNameParts = []string{"database", "cluster", "namespace", "role"}

func (m databaseBindingNameDefaultModifier) Description(_ context.Context) string {
	return `Defaults to "{database}-{cluster}-{namespace}-{role}".`
}

func (m databaseBindingNameDefaultModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m databaseBindingNameDefaultModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	if !req.State.Raw.IsNull() {
		resp.PlanValue = req.StateValue
		return
	}
	parts := make([]string, 0, len(databaseBindingNameParts))
	for _, attr := range databaseBindingNameParts {
		var v types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(attr), &v)...)
		if resp.Diagnostics.HasError() || v.IsUnknown() || v.IsNull() {
			return
		}
		parts = append(parts, v.ValueString())
	}
	name := strings.Join(parts, "-")
	if len(name) > maxNameLength {
		resp.Diagnostics.AddAttributeError(req.Path,
			"Default binding name too long",
			fmt.Sprintf("The default name %q is longer than %d characters. Set `name` explicitly.", name, maxNameLength))
		return
	}
	resp.PlanValue = types.StringValue(name)
}
