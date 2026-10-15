package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Schema attributes several resources share. Each resource still writes
// its own schema map and picks these where they fit.

// etagAttribute is the ETag of the last read or write, sent as If-Match on
// updates.
func etagAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		// No UseStateForUnknown: it changes on every write, so the plan
		// honestly says "known after apply" on any update.
		Description: "Resource version used for optimistic locking during updates.",
		Computed:    true,
	}
}

// createdAtAttribute is the creation timestamp of what, e.g. "the database".
func createdAtAttribute(what string) schema.StringAttribute {
	return schema.StringAttribute{
		Description: "Timestamp when " + what + " was created.",
		Computed:    true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
		},
	}
}

// ResourcesModel is the `resources` block: CPU, memory and storage in
// Kubernetes quantity format.
type ResourcesModel struct {
	CPU     types.String `tfsdk:"cpu"`
	Memory  types.String `tfsdk:"memory"`
	Storage types.String `tfsdk:"storage"`
}

// requiredResourcesAttribute is a `resources` block whose three quantities
// are all required and validated, and whose storage can only grow: the
// per-instance size of a service the API sizes in full (a database), unlike
// a cluster's optional limits.
func requiredResourcesAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Description: description,
		Required:    true,
		Attributes: map[string]schema.Attribute{
			"cpu": schema.StringAttribute{
				Description: "CPU without a unit, e.g. `500m` or `2`.",
				Required:    true,
				Validators:  cpuQuantityValidators(),
			},
			"memory": schema.StringAttribute{
				Description: "Memory with a unit, e.g. `1Gi` or `512Mi`.",
				Required:    true,
				Validators:  sizeQuantityValidators(),
			},
			"storage": schema.StringAttribute{
				Description: "Storage with a unit, e.g. `10Gi`. Storage can only grow: a smaller value fails the plan.",
				Required:    true,
				Validators:  sizeQuantityValidators(),
				PlanModifiers: []planmodifier.String{
					storageNeverShrinks(),
				},
			},
		},
	}
}

// resourcesFromPlan builds a ResourceQuantities only including
// non-null fields so omitted nested values are absent from the JSON body
// rather than sent as empty strings.
func resourcesFromPlan(m *ResourcesModel) *client.ResourceQuantities {
	r := &client.ResourceQuantities{}
	if !m.CPU.IsNull() && !m.CPU.IsUnknown() {
		r.CPU = m.CPU.ValueString()
	}
	if !m.Memory.IsNull() && !m.Memory.IsUnknown() {
		r.Memory = m.Memory.ValueString()
	}
	if !m.Storage.IsNull() && !m.Storage.IsUnknown() {
		r.Storage = m.Storage.ValueString()
	}
	return r
}

// resourcesEqual reports whether two `resources` blocks hold the same
// values; two absent blocks are equal.
func resourcesEqual(a, b *ResourcesModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.CPU.Equal(b.CPU) && a.Memory.Equal(b.Memory) && a.Storage.Equal(b.Storage)
}

// stringOrNull returns types.StringNull for empty API values so terraform
// state doesn't contain "" for fields the user never set — that prevents
// perpetual diffs on partial resources blocks (e.g., memory-only).
func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
