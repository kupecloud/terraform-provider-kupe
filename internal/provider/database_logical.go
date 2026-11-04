package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var logicalDatabaseType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name": types.StringType, "schemas": types.ListType{ElemType: postgresNameType},
}}

const logicalDatabasesDescription = "PostgreSQL databases on this instance, including its first database: 1 to 10 entries. For a new empty instance, the first name is the instance name with hyphens replaced by underscores (orders-db becomes orders_db). A restored instance keeps its source's first PostgreSQL name. Database names are at most 53 characters. Databases and schemas can only be added; the first entry cannot move. Use an owner binding to run grants inside added databases. Ready confirms provisioning, not tenant SQL grants. Restore first without this list, then update it using the effective first name."

func logicalNameValidators(maxLength int) []validator.String {
	return []validator.String{stringvalidator.LengthBetween(1, maxLength), stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z][a-z0-9_]*$`), "must start with a lowercase letter and contain only lowercase letters, numbers or _")}
}
func logicalDatabasesSchema() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{Optional: true, Computed: true, Description: logicalDatabasesDescription,
		PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown(), logicalDatabasesOnlyGrow()},
		Validators:    []validator.List{listvalidator.SizeBetween(1, 10), logicalNamesValidator{}},
		NestedObject: schema.NestedAttributeObject{Validators: []validator.Object{logicalEntryValidator{max: 53}}, Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Optional: true, Description: "PostgreSQL database name. A name is required when the entry is known. Added databases cannot use names reserved by PostgreSQL or the platform.", Validators: logicalNameValidators(53)},
			"schemas": schema.ListNestedAttribute{Optional: true, Computed: true, Description: "Schemas to create in this database: at most 20. Schemas can only be added. Names are at most 63 characters; public, information_schema and names starting with pg_ are reserved.", Validators: []validator.List{listvalidator.SizeAtMost(20)}, NestedObject: schema.NestedAttributeObject{Validators: []validator.Object{logicalEntryValidator{max: 63}}, Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{Optional: true, Description: "PostgreSQL schema name. A name is required when the entry is known.", Validators: logicalNameValidators(63)},
			}}},
		}},
	}
}
func postgresDatabases(obj types.Object) types.List {
	if obj.IsUnknown() {
		return types.ListUnknown(logicalDatabaseType)
	}
	if obj.IsNull() {
		return types.ListNull(logicalDatabaseType)
	}
	if v, ok := obj.Attributes()["databases"].(types.List); ok {
		return v
	}
	return types.ListNull(logicalDatabaseType)
}
func logicalDatabasesFromValue(list types.List) []client.LogicalDatabase {
	result := make([]client.LogicalDatabase, 0, len(list.Elements()))
	for _, v := range list.Elements() {
		o, ok := v.(types.Object)
		if !ok || !isSet(o) {
			result = append(result, client.LogicalDatabase{})
			continue
		}
		n, _ := o.Attributes()["name"].(types.String)
		schemas, _ := o.Attributes()["schemas"].(types.List)
		db := client.LogicalDatabase{Name: n.ValueString(), Schemas: []client.DatabaseSchema{}}
		for _, sv := range schemas.Elements() {
			if so, ok := sv.(types.Object); ok && isSet(so) {
				sn, _ := so.Attributes()["name"].(types.String)
				db.Schemas = append(db.Schemas, client.DatabaseSchema{Name: sn.ValueString()})
			}
		}
		result = append(result, db)
	}
	return result
}
func logicalDatabasesValue(list *[]client.LogicalDatabase) types.List {
	if list == nil {
		return types.ListNull(logicalDatabaseType)
	}
	values := make([]attr.Value, 0, len(*list))
	for _, db := range *list {
		schemas := make([]attr.Value, 0, len(db.Schemas))
		for _, s := range db.Schemas {
			schemas = append(schemas, types.ObjectValueMust(postgresNameType.AttrTypes, map[string]attr.Value{"name": types.StringValue(s.Name)}))
		}
		values = append(values, types.ObjectValueMust(logicalDatabaseType.AttrTypes, map[string]attr.Value{"name": types.StringValue(db.Name), "schemas": types.ListValueMust(postgresNameType, schemas)}))
	}
	return types.ListValueMust(logicalDatabaseType, values)
}

type logicalNamesValidator struct{}

func (logicalNamesValidator) Description(context.Context) string {
	return "Database and schema names must be distinct and must not be reserved."
}
func (v logicalNamesValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (logicalNamesValidator) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	resp.Diagnostics.Append(validateLogicalValues(ctx, req.ConfigValue, req.Path)...)
}
func validateLogicalValues(ctx context.Context, list types.List, p path.Path) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	if isSet(list) {
		if len(list.Elements()) < 1 || len(list.Elements()) > 10 {
			diagnostics.AddAttributeError(p, "Invalid database count", "Declare at least 1 and at most 10 PostgreSQL databases.")
		}
		for i, value := range list.Elements() {
			obj, ok := value.(types.Object)
			if !ok {
				continue
			}
			diagnostics.Append(validateLogicalEntry(ctx, obj, p.AtListIndex(i), 53)...)
			if !isSet(obj) {
				continue
			}
			schemas, _ := obj.Attributes()["schemas"].(types.List)
			if len(schemas.Elements()) > 20 {
				diagnostics.AddAttributeError(p.AtListIndex(i).AtName("schemas"), "Invalid schema count", "Declare at most 20 schemas per PostgreSQL database.")
			}
			for j, value := range schemas.Elements() {
				if entry, ok := value.(types.Object); ok {
					diagnostics.Append(validateLogicalEntry(ctx, entry, p.AtListIndex(i).AtName("schemas").AtListIndex(j), 63)...)
				}
			}
		}
	}
	if !isSet(list) {
		return diagnostics
	}
	seen := map[string]bool{}
	for i, db := range logicalDatabasesFromValue(list) {
		if db.Name != "" {
			reserved := slices.Contains([]string{"postgres", "template0", "template1", "all", "sameuser", "samerole", "samegroup", "replication"}, db.Name)
			if i > 0 {
				reserved = reserved || db.Name == "app"
				for _, prefix := range []string{"pg_", "cnpg_", "mdba_", "mdbb_", "mdbl_"} {
					reserved = reserved || strings.HasPrefix(db.Name+"_", prefix)
				}
			}
			if reserved {
				diagnostics.AddAttributeError(p.AtListIndex(i).AtName("name"), "Reserved database name", reservedLogicalDatabaseMessage(i, db.Name))
			}
			if seen[db.Name] {
				diagnostics.AddAttributeError(p, "Duplicate database name", fmt.Sprintf("Database %q is listed more than once.", db.Name))
			}
			seen[db.Name] = true
		}
		schemas := map[string]bool{}
		for _, s := range db.Schemas {
			if s.Name == "" {
				continue
			}
			if schemas[s.Name] || s.Name == "public" || s.Name == "information_schema" || strings.HasPrefix(s.Name, "pg_") {
				diagnostics.AddAttributeError(p.AtListIndex(i).AtName("schemas"), "Invalid schema name", fmt.Sprintf("Schema name %q is duplicated or reserved. Choose another name.", s.Name))
			}
			schemas[s.Name] = true
		}
	}
	return diagnostics
}

type logicalEntryValidator struct{ max int }

func (v logicalEntryValidator) Description(context.Context) string {
	return "Known database and schema entries require a valid name."
}
func (v logicalEntryValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v logicalEntryValidator) ValidateObject(ctx context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	resp.Diagnostics.Append(validateLogicalEntry(ctx, req.ConfigValue, req.Path, v.max)...)
}
func validateLogicalEntry(_ context.Context, obj types.Object, p path.Path, maxLength int) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	if obj.IsUnknown() {
		return diagnostics
	}
	if obj.IsNull() {
		diagnostics.AddAttributeError(p, "Invalid PostgreSQL entry", "A database or schema entry cannot be null. Supply an object with a name.")
		return diagnostics
	}
	name, ok := obj.Attributes()["name"].(types.String)
	if ok && name.IsUnknown() {
		return diagnostics
	}
	if !ok || name.IsNull() || name.ValueString() == "" {
		diagnostics.AddAttributeError(p.AtName("name"), "PostgreSQL name required", "A known database or schema entry must have a non-null, non-empty name.")
		return diagnostics
	}
	if len(name.ValueString()) > maxLength || !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(name.ValueString()) {
		diagnostics.AddAttributeError(p.AtName("name"), "Invalid PostgreSQL name", fmt.Sprintf("Names must start with a lowercase letter, contain only lowercase letters, numbers or _, and be at most %d characters.", maxLength))
	}
	return diagnostics
}

func logicalDatabasesOnlyGrow() planmodifier.List { return logicalDatabasesOnlyGrowModifier{} }

type logicalDatabasesOnlyGrowModifier struct{}

func (logicalDatabasesOnlyGrowModifier) Description(context.Context) string {
	return "Logical databases and schemas can only grow; the first database keeps its identity."
}
func (m logicalDatabasesOnlyGrowModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (logicalDatabasesOnlyGrowModifier) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	// The immutable resource name can already require a new instance. Its
	// first database and omitted schemas belong to that new instance, not
	// to the old instance's effective list. Match the existing name
	// RequiresReplace equality test, including an unknown planned name.
	var instanceName types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &instanceName)...)
	creating := req.State.Raw.IsNull()
	if !creating {
		var previousName types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &previousName)...)
		creating = !instanceName.Equal(previousName)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if creating && isSet(req.ConfigValue) {
		var source types.Object
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("source"), &source)...)
		if !source.IsNull() {
			resp.Diagnostics.AddAttributeError(req.Path, "Restore database settings after creation", "Create the restored database without postgres.databases. After the restore, read its effective PostgreSQL names and add databases or schemas in a separate update.")
			return
		}
	}
	resp.Diagnostics.Append(validateLogicalValues(ctx, req.PlanValue, req.Path)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if creating && req.ConfigValue.IsNull() {
		// Terraform replans computed defaults with null state for a
		// replacement. Do not constrain them to this instance's old list.
		return
	}
	if req.PlanValue.IsUnknown() {
		return
	}
	if req.PlanValue.IsNull() {
		if isSet(req.StateValue) {
			resp.PlanValue = req.StateValue
		}
		return
	}
	// Adopt omitted schema lists by database name, rather than by list index.
	// Reordering added databases must not copy another database's schemas.
	values := req.PlanValue.Elements()
	for i, value := range values {
		obj, ok := value.(types.Object)
		if !ok || !isSet(obj) {
			continue
		}
		attrs := obj.Attributes()
		name, _ := attrs["name"].(types.String)
		var configured types.List
		if i < len(req.ConfigValue.Elements()) {
			if co, ok := req.ConfigValue.Elements()[i].(types.Object); ok {
				configured, _ = co.Attributes()["schemas"].(types.List)
			}
		}
		if !configured.IsNull() {
			continue
		}
		if !isSet(name) {
			attrs["schemas"] = types.ListUnknown(postgresNameType)
			values[i] = types.ObjectValueMust(logicalDatabaseType.AttrTypes, attrs)
			continue
		}
		schemas := types.ListValueMust(postgresNameType, []attr.Value{})
		var previous []attr.Value
		if !creating {
			previous = req.StateValue.Elements()
		}
		for _, old := range previous {
			if oo, ok := old.(types.Object); ok && isSet(oo) {
				n, _ := oo.Attributes()["name"].(types.String)
				if n.Equal(name) {
					schemas, _ = oo.Attributes()["schemas"].(types.List)
				}
			}
		}
		attrs["schemas"] = schemas
		values[i] = types.ObjectValueMust(logicalDatabaseType.AttrTypes, attrs)
	}
	req.PlanValue = types.ListValueMust(logicalDatabaseType, values)
	resp.PlanValue = req.PlanValue
	planned := logicalDatabasesFromValue(req.PlanValue)
	if len(planned) == 0 {
		return
	} // SizeBetween reports an explicit empty list.
	var first string
	if creating {
		if !isSet(instanceName) {
			return
		}
		first = strings.ReplaceAll(instanceName.ValueString(), "-", "_")
	} else {
		previous := logicalDatabasesFromValue(req.StateValue)
		if len(previous) == 0 {
			resp.Diagnostics.AddAttributeError(req.Path, "PostgreSQL identity unavailable", "Refresh the database to confirm its first PostgreSQL name before declaring databases or schemas.")
			return
		}
		first = previous[0].Name
	}
	resp.Diagnostics.Append(validateLogicalTransition(req.PlanValue, req.StateValue, first, creating, req.Path)...)
}
func validateLogicalTransition(list, prior types.List, first string, creating bool, p path.Path) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	planned := logicalDatabasesFromValue(list)
	if len(planned) == 0 {
		return diagnostics
	}
	if planned[0].Name != "" && planned[0].Name != first {
		diagnostics.AddAttributeError(p, "First PostgreSQL database cannot change", fmt.Sprintf("The first entry must be %q, this instance's first PostgreSQL database. Keep it first; changing its identity is not supported.", first))
	}
	if planned[0].Name == "" {
		for _, db := range planned[1:] {
			if db.Name == first {
				diagnostics.AddAttributeError(p, "First PostgreSQL database cannot change", fmt.Sprintf("The first entry must be %q. It cannot also appear later in the list; keep this instance's first PostgreSQL database first.", first))
				return diagnostics
			}
		}
	}
	if creating {
		return diagnostics
	}
	previous := logicalDatabasesFromValue(prior)
	names := make([]string, 0, len(previous))
	for _, db := range previous {
		names = append(names, db.Name)
	}
	if unknownNamesCannotRetain(names, list) || unknownDatabaseSlotsCannotRetain(previous, list, first) {
		diagnostics.AddAttributeError(p, "Logical databases and schemas cannot be removed", "This list leaves out databases that already exist, even once its unknown names are known. Add the existing entries back.")
		return diagnostics
	}
	for _, old := range previous {
		idx := slices.IndexFunc(planned, func(db client.LogicalDatabase) bool { return db.Name == old.Name })
		if idx < 0 {
			if logicalListUnknown(list) {
				continue
			}
			logicalRemovalAt(&diagnostics, p, old.Name)
			continue
		}
		obj := list.Elements()[idx].(types.Object)
		schemas, _ := obj.Attributes()["schemas"].(types.List)
		schemaNames := make([]string, 0, len(old.Schemas))
		for _, s := range old.Schemas {
			schemaNames = append(schemaNames, s.Name)
		}
		if unknownNamesCannotRetain(schemaNames, schemas) {
			diagnostics.AddAttributeError(p.AtListIndex(idx).AtName("schemas"), "Logical databases and schemas cannot be removed", fmt.Sprintf("This list leaves out schemas of %q that already exist, even once its unknown names are known. Add the existing entries back.", old.Name))
			continue
		}
		if schemas.IsUnknown() || logicalListUnknown(schemas) {
			continue
		}
		for _, s := range old.Schemas {
			if !slices.ContainsFunc(planned[idx].Schemas, func(v client.DatabaseSchema) bool { return v.Name == s.Name }) {
				logicalRemovalAt(&diagnostics, p, old.Name+"."+s.Name)
			}
		}
	}
	return diagnostics
}

func logicalRemovalAt(diagnostics *diag.Diagnostics, p path.Path, name string) {
	diagnostics.AddAttributeError(p, "Logical databases and schemas cannot be removed", fmt.Sprintf("Logical databases and schemas cannot be removed: %q is in state but not in configuration. Add it back; removing a logical database or schema is not supported.", name))
}

func logicalListUnknown(v types.List) bool {
	for _, e := range v.Elements() {
		o, ok := e.(types.Object)
		if !ok || o.IsUnknown() {
			return true
		}
		n, _ := o.Attributes()["name"].(types.String)
		if n.IsUnknown() {
			return true
		}
	}
	return false
}

// Mirror the API/operator PD52 denials for names which generate reserved roles.
func reservedLogicalDatabaseMessage(index int, name string) string {
	if index > 0 {
		reserved := name == "app"
		for _, prefix := range []string{"pg_", "cnpg_", "mdba_", "mdbb_", "mdbl_"} {
			reserved = reserved || strings.HasPrefix(name+"_", prefix)
		}
		if reserved {
			return fmt.Sprintf("%q is reserved: added databases cannot be named app, pg, cnpg, mdba, mdbb or mdbl, or start with pg_, cnpg_, mdba_, mdbb_ or mdbl_.", name)
		}
	}

	return fmt.Sprintf("database name %q is reserved for PostgreSQL", name)
}

// Every unresolved name can retain at most one existing entry. A partially
// unknown list must still fail now when no resolution could retain them all.
func unknownNamesCannotRetain(previous []string, planned types.List) bool {
	return logicalListUnknown(planned) && !logicalNamesRetentionFeasible(previous, planned)
}

// Omitted/computed lists remain possible; each unresolved entry can retain one
// missing name. Explicit empty lists have no such capacity.
func logicalNamesRetentionFeasible(previous []string, planned types.List) bool {
	if planned.IsUnknown() || planned.IsNull() {
		return true
	}
	known := map[string]bool{}
	unknown := 0
	for _, v := range planned.Elements() {
		o, ok := v.(types.Object)
		if !ok || o.IsUnknown() {
			unknown++
			continue
		}
		n, _ := o.Attributes()["name"].(types.String)
		if n.IsUnknown() {
			unknown++
		} else {
			known[n.ValueString()] = true
		}
	}
	missing := 0
	for _, name := range previous {
		if !known[name] {
			missing++
		}
	}
	return missing <= unknown
}

// Unknown database names must have a joint assignment that preserves each
// missing database and its schemas. A slot cannot retain two databases, and
// the first slot is reserved for this instance's first database. Augmenting
// paths allow an earlier flexible choice to move instead of rejecting a valid
// alternative (the schema limits the list to ten databases).
func unknownDatabaseSlotsCannotRetain(previous []client.LogicalDatabase, planned types.List, first string) bool {
	if planned.IsUnknown() || planned.IsNull() {
		return false
	}
	known := map[string]bool{}
	var slots []int
	for i, value := range planned.Elements() {
		obj, ok := value.(types.Object)
		if !ok || obj.IsNull() {
			continue
		}
		name, _ := obj.Attributes()["name"].(types.String)
		if obj.IsUnknown() || name.IsUnknown() {
			slots = append(slots, i)
		} else {
			known[name.ValueString()] = true
		}
	}
	if len(slots) == 0 {
		return false
	}
	var edges [][]int
	for _, old := range previous {
		if known[old.Name] {
			continue
		}
		names := make([]string, 0, len(old.Schemas))
		for _, s := range old.Schemas {
			names = append(names, s.Name)
		}
		var compatible []int
		for slot, index := range slots {
			if (index == 0) != (old.Name == first) {
				continue
			}
			obj := planned.Elements()[index].(types.Object)
			if obj.IsUnknown() {
				compatible = append(compatible, slot)
				continue
			}
			schemas, _ := obj.Attributes()["schemas"].(types.List)
			if logicalNamesRetentionFeasible(names, schemas) {
				compatible = append(compatible, slot)
			}
		}
		edges = append(edges, compatible)
	}
	owners := make([]int, len(slots))
	for i := range owners {
		owners[i] = -1
	}
	var assign func(int, []bool) bool
	assign = func(database int, seen []bool) bool {
		for _, slot := range edges[database] {
			if seen[slot] {
				continue
			}
			seen[slot] = true
			if owners[slot] < 0 || assign(owners[slot], seen) {
				owners[slot] = database
				return true
			}
		}
		return false
	}
	for database := range edges {
		if !assign(database, make([]bool, len(slots))) {
			return true
		}
	}
	return false
}

// Validate the final values again before serialisation: configuration validation
// can run before outputs have resolved, and null/unknown entries lose their
// shape when converted to the API model.
func validateLogicalWrite(ctx context.Context, config tfsdk.Config, plan, prior DatabaseResourceModel, creating bool) diag.Diagnostics {
	list := postgresDatabases(plan.Postgres)
	p := path.Root("postgres").AtName("databases")
	diagnostics := validateLogicalValues(ctx, list, p)
	var configured types.Object
	diagnostics.Append(config.GetAttribute(ctx, path.Root("postgres"), &configured)...)
	if list.IsUnknown() && !postgresDatabases(configured).IsNull() {
		diagnostics.AddAttributeError(p, "PostgreSQL names unavailable", "Resolve all database and schema entry names before applying this change.")
	}
	if list.IsNull() || list.IsUnknown() {
		return diagnostics
	} // Omitted computed list uses API defaults.
	unresolved := logicalListUnknown(list)
	for _, value := range list.Elements() {
		if obj, ok := value.(types.Object); ok && isSet(obj) {
			schemas, _ := obj.Attributes()["schemas"].(types.List)
			unresolved = unresolved || schemas.IsUnknown() || logicalListUnknown(schemas)
		}
	}
	if unresolved {
		diagnostics.AddAttributeError(p, "PostgreSQL names unavailable", "Resolve all database and schema entry names before applying this change.")
	}
	if diagnostics.HasError() {
		return diagnostics
	}
	first := strings.ReplaceAll(plan.Name.ValueString(), "-", "_")
	old := postgresDatabases(prior.Postgres)
	if !creating {
		previous := logicalDatabasesFromValue(old)
		if len(previous) == 0 {
			diagnostics.AddAttributeError(p, "PostgreSQL identity unavailable", "Refresh the database to confirm its first PostgreSQL name before declaring databases or schemas.")
			return diagnostics
		}
		first = previous[0].Name
	} else if !plan.Source.IsNull() {
		diagnostics.AddAttributeError(p, "Restore database settings after creation", "Create the restored database without postgres.databases. After the restore, read its effective PostgreSQL names and add databases or schemas in a separate update.")
		return diagnostics
	}
	diagnostics.Append(validateLogicalTransition(list, old, first, creating, p)...)
	return diagnostics
}
