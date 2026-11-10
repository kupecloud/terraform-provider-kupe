package provider

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var postgresNameType = types.ObjectType{AttrTypes: map[string]attr.Type{"name": types.StringType}}

// databasePoolerAttrTypes are the attributes of `postgres.pooler`.
var databasePoolerAttrTypes = map[string]attr.Type{
	"enabled":   types.BoolType,
	"pool_mode": types.StringType,
	"instances": types.Int64Type,
}

var databasePostgresAttrTypes = map[string]attr.Type{
	"pooler":     types.ObjectType{AttrTypes: databasePoolerAttrTypes},
	"databases":  types.ListType{ElemType: logicalDatabaseType},
	"roles":      types.SetType{ElemType: postgresNameType},
	"extensions": types.SetType{ElemType: postgresNameType},
}

const postgresDescription = "PostgreSQL databases, group roles, extensions and the connection pooler. Omitted settings keep their current values. Roles and extensions can be cleared with []; databases and schemas can only grow. The platform grants declared roles no privileges."

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
	return schema.SingleNestedAttribute{Optional: true, Computed: true, Description: postgresDescription, PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()}, Attributes: map[string]schema.Attribute{"roles": list(true), "extensions": list(false), "databases": logicalDatabasesSchema(), "pooler": poolerSchema()}}
}

// Descriptions of the `postgres.pooler` attributes.
const (
	poolerDescription = "Connection pooler. Turn it on if your app opens a lot of connections: many pods, " +
		"serverless functions, or short-lived connections. Small apps with a few steady connections don't " +
		"need it. Applications of `readwrite`, `readonly` and custom-role bindings connect through the " +
		"pooler once it serves, and owner bindings connect to the database directly, so turning the pooler " +
		"on or off changes the host those bindings use in each namespace (`namespace_status[].host` of " +
		"`kupe_database_binding`). It can be turned on or off in place. Removing `pooler` from the " +
		"configuration keeps the current setting: set `enabled = false` to turn it off. The platform sets the " +
		"pool sizes. Connection pooling may not be available on every Kupe platform: turning it on or " +
		"changing it then fails with error code `DATABASE_POOLER_UNAVAILABLE`."
	poolerEnabledDescription  = "Run the pooler. `false` turns it off."
	poolerPoolModeDescription = "How server connections are shared: `transaction` (the default) or `session`. " +
		"`transaction` does not keep session state between transactions: `SET`, SQL `PREPARE`, `LISTEN`, " +
		"session advisory locks and `WITH HOLD` cursors need `session`."
	poolerInstancesDescription = "Number of pooler instances: 1 (the default) or 2."
	poolerReadDescription      = "Connection pooler settings. Off by default."
)

func poolerSchema() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Description: poolerDescription,
		Optional:    true,
		Computed:    true,
		// Unset in configuration, the current setting stays: the platform
		// never changes it on its own.
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		Attributes: map[string]schema.Attribute{
			"enabled": schema.BoolAttribute{Required: true, Description: poolerEnabledDescription},
			"pool_mode": schema.StringAttribute{
				Description:   poolerPoolModeDescription,
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf("transaction", "session")},
			},
			"instances": schema.Int64Attribute{
				Description:   poolerInstancesDescription,
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Validators:    []validator.Int64{int64validator.Between(1, 2)},
			},
		},
	}
}

// postgresPooler is the `pooler` object of a `postgres` object: unknown
// when postgres is unknown, null when it is null or has none.
func postgresPooler(obj types.Object) types.Object {
	if obj.IsUnknown() {
		return types.ObjectUnknown(databasePoolerAttrTypes)
	}
	if obj.IsNull() {
		return types.ObjectNull(databasePoolerAttrTypes)
	}
	v, ok := obj.Attributes()["pooler"].(types.Object)
	if !ok {
		return types.ObjectNull(databasePoolerAttrTypes)
	}
	return v
}

// poolerAttributes is the enabled, pool_mode and instances of a `pooler`
// object: unknown when it is unknown, null when it is null.
func poolerAttributes(pooler types.Object) (types.Bool, types.String, types.Int64) {
	if pooler.IsUnknown() {
		return types.BoolUnknown(), types.StringUnknown(), types.Int64Unknown()
	}
	if pooler.IsNull() {
		return types.BoolNull(), types.StringNull(), types.Int64Null()
	}
	attrs := pooler.Attributes()
	enabled, _ := attrs["enabled"].(types.Bool)
	mode, _ := attrs["pool_mode"].(types.String)
	instances, _ := attrs["instances"].(types.Int64)
	return enabled, mode, instances
}

// poolerChanges is the pooler keys to send: the planned keys with a known
// value that differs from prior's. nil when none does, so an unchanged
// pooler never reaches the wire and the API is never sent an empty one.
func poolerChanges(plan, prior types.Object) *client.DatabasePooler {
	enabled, mode, instances := poolerAttributes(plan)
	priorEnabled, priorMode, priorInstances := poolerAttributes(prior)
	var pooler client.DatabasePooler
	changed := false
	if isSet(enabled) && !enabled.Equal(priorEnabled) {
		v := enabled.ValueBool()
		pooler.Enabled = &v
		changed = true
	}
	if isSet(mode) && !mode.Equal(priorMode) {
		pooler.PoolMode = mode.ValueString()
		changed = true
	}
	if isSet(instances) && !instances.Equal(priorInstances) {
		pooler.Instances = int32(instances.ValueInt64()) // #nosec G115 -- the schema validates 1 to 2
		changed = true
	}
	if !changed {
		return nil
	}
	return &pooler
}

// poolerFromAPI is the `pooler` object for the API's settings. The API
// sends every field with its default filled in; an API that sends none
// reads as off, with the mode and instances null.
func poolerFromAPI(p *client.DatabasePooler) types.Object {
	enabled, mode, instances := types.BoolValue(false), types.StringNull(), types.Int64Null()
	if p != nil {
		if p.Enabled != nil {
			enabled = types.BoolValue(*p.Enabled)
		}
		mode = stringOrNull(p.PoolMode)
		if p.Instances != 0 {
			instances = types.Int64Value(int64(p.Instances))
		}
	}
	return types.ObjectValueMust(databasePoolerAttrTypes, map[string]attr.Value{
		"enabled": enabled, "pool_mode": mode, "instances": instances,
	})
}

// poolerOn reports whether the database's pooler is on.
func poolerOn(db *client.Database) bool {
	return db.Postgres != nil && db.Postgres.Pooler != nil && db.Postgres.Pooler.Enabled != nil && *db.Postgres.Pooler.Enabled
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
	p.Pooler = poolerChanges(postgresPooler(plan), postgresPooler(prior))
	if p.Roles == nil && p.Extensions == nil && p.Databases == nil && p.Pooler == nil {
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
	return types.ObjectValueMust(databasePostgresAttrTypes, map[string]attr.Value{"roles": list(roles), "extensions": list(extensions), "databases": logicalDatabasesValue(p.Databases), "pooler": poolerFromAPI(p.Pooler)})
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
