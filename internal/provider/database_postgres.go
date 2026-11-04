package provider

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var postgresNameType = types.ObjectType{AttrTypes: map[string]attr.Type{"name": types.StringType}}
var databasePostgresAttrTypes = map[string]attr.Type{
	"databases":  types.ListType{ElemType: logicalDatabaseType},
	"roles":      types.SetType{ElemType: postgresNameType},
	"extensions": types.SetType{ElemType: postgresNameType},
}

const postgresDescription = "PostgreSQL databases, group roles and extensions. Omitted settings keep their current values. Roles and extensions can be cleared with []; databases and schemas can only grow. The platform grants declared roles no privileges."

func databasePostgresSchema() schema.SingleNestedAttribute {
	list := func(role bool) schema.SetNestedAttribute {
		pattern, cap := `^[a-z][a-z0-9_-]*$`, 32
		validators := []validator.String{}
		if role {
			pattern, cap = `^[a-z][a-z0-9_]*$`, 20
			validators = append(validators, reservedDatabaseRole{})
		}
		validators = append(validators, stringvalidator.LengthBetween(1, 63), stringvalidator.RegexMatches(regexp.MustCompile(pattern), "must start with a lowercase letter and contain only lowercase letters, numbers and the allowed separators"))
		return schema.SetNestedAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}, Validators: []validator.Set{setvalidator.SizeAtMost(cap)}, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, Validators: validators, Description: "Name declared in PostgreSQL."},
		}}}
	}
	return schema.SingleNestedAttribute{Optional: true, Computed: true, Description: postgresDescription, PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()}, Attributes: map[string]schema.Attribute{"roles": list(true), "extensions": list(false), "databases": logicalDatabasesSchema()}}
}

type reservedDatabaseRole struct{}

func (reservedDatabaseRole) Description(context.Context) string {
	return "must not use a reserved PostgreSQL role name, prefix or suffix"
}
func (v reservedDatabaseRole) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (reservedDatabaseRole) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !isSet(req.ConfigValue) {
		return
	}
	name := req.ConfigValue.ValueString()
	if slices.Contains([]string{"app_owner", "app_readwrite", "app_readonly", "owner", "readwrite", "readonly", "custom", "postgres", "streaming_replica", "public", "none", "current_user", "session_user", "current_role", "all", "sameuser", "samerole", "replication"}, name) {
		resp.Diagnostics.AddAttributeError(req.Path, "Reserved role name", "Choose a role name that is not reserved by the platform.")
		return
	}
	for _, prefix := range []string{"pg_", "cnpg_", "mdbb_", "mdba_", "mdbl_"} {
		if strings.HasPrefix(name, prefix) {
			resp.Diagnostics.AddAttributeError(req.Path, "Reserved role name", "Choose a role name that is not reserved by the platform.")
			return
		}
	}
	for _, suffix := range []string{"_owner", "_readwrite", "_readonly"} {
		if strings.HasSuffix(name, suffix) {
			resp.Diagnostics.AddAttributeError(req.Path, "Reserved role name", "Declared role names ending in _owner, _readwrite or _readonly are reserved for database logins.")
			return
		}
	}
}

func postgresList(obj types.Object, key string) types.Set {
	if obj.IsUnknown() {
		return types.SetUnknown(postgresNameType)
	}
	if obj.IsNull() {
		return types.SetNull(postgresNameType)
	}
	v, _ := obj.Attributes()[key].(types.Set)
	return v
}
func postgresNames(set types.Set) []string {
	names := make([]string, 0, len(set.Elements()))
	for _, v := range set.Elements() {
		if o, ok := v.(types.Object); ok && isSet(o) {
			if n, ok := o.Attributes()["name"].(types.String); ok && isSet(n) {
				names = append(names, n.ValueString())
			}
		}
	}
	slices.Sort(names)
	return names
}
func postgresChanges(plan, prior types.Object) *client.DatabasePostgres {
	var p client.DatabasePostgres
	roles, oldRoles := postgresList(plan, "roles"), postgresList(prior, "roles")
	if isSet(roles) && !roles.Equal(oldRoles) {
		v := make([]client.DeclaredRole, 0, len(roles.Elements()))
		for _, n := range postgresNames(roles) {
			v = append(v, client.DeclaredRole{Name: n})
		}
		p.Roles = &v
	}
	extensions, oldExtensions := postgresList(plan, "extensions"), postgresList(prior, "extensions")
	if isSet(extensions) && !extensions.Equal(oldExtensions) {
		v := make([]client.Extension, 0, len(extensions.Elements()))
		for _, n := range postgresNames(extensions) {
			v = append(v, client.Extension{Name: n})
		}
		p.Extensions = &v
	}
	databases, oldDatabases := postgresDatabases(plan), postgresDatabases(prior)
	if isSet(databases) && !databases.Equal(oldDatabases) {
		v := logicalDatabasesFromValue(databases)
		p.Databases = &v
	}
	if p.Roles == nil && p.Extensions == nil && p.Databases == nil {
		return nil
	}
	return &p
}
func postgresFromAPI(p *client.DatabasePostgres) types.Object {
	if p == nil {
		return types.ObjectNull(databasePostgresAttrTypes)
	}
	list := func(names []string) types.Set {
		values := make([]attr.Value, 0, len(names))
		for _, n := range names {
			values = append(values, types.ObjectValueMust(postgresNameType.AttrTypes, map[string]attr.Value{"name": types.StringValue(n)}))
		}
		return types.SetValueMust(postgresNameType, values)
	}
	var roles, extensions []string
	if p.Roles != nil {
		for _, r := range *p.Roles {
			roles = append(roles, r.Name)
		}
	}
	if p.Extensions != nil {
		for _, e := range *p.Extensions {
			extensions = append(extensions, e.Name)
		}
	}
	return types.ObjectValueMust(databasePostgresAttrTypes, map[string]attr.Value{"roles": list(roles), "extensions": list(extensions), "databases": logicalDatabasesValue(p.Databases)})
}

// ModifyPlan keeps read-only values known when filling an omitted postgres
// list from state is the only proposed change. The framework marks computed
// fields unknown before nested plan modifiers restore those omitted lists.
func (r *DatabaseResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || len(resp.RequiresReplace) > 0 {
		return
	}
	var plan, state DatabaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A computed PostgreSQL object makes the framework revisit other computed
	// fields. Keep an omitted backup key null when the API has never set it.
	if req.Config.Schema != nil && isSet(plan.Backup) && isSet(state.Backup) {
		var configured types.Object
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("backup"), &configured)...)
		if isSet(configured) {
			values := plan.Backup.Attributes()
			for _, key := range []string{"retention", "schedule"} {
				if configured.Attributes()[key].IsNull() && values[key].IsUnknown() {
					values[key] = state.Backup.Attributes()[key]
				}
			}
			plan.Backup = types.ObjectValueMust(databaseBackupAttrTypes, values)
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("backup"), plan.Backup)...)
		}
	}
	if len(settingsChanged(&state.databaseModel, plan.databaseModel)) != 0 || !plan.Name.Equal(state.Name) || !plan.Engine.Equal(state.Engine) || !plan.Source.Equal(state.Source) {
		return
	}
	plan.Phase = state.Phase
	plan.CurrentVersion = state.CurrentVersion
	plan.LastBackupAt = state.LastBackupAt
	plan.RecoveryWindow = state.RecoveryWindow
	plan.BackupSizeBytes = state.BackupSizeBytes
	plan.CreatedAt = state.CreatedAt
	plan.ETag = state.ETag
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}
