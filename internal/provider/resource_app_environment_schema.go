package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// Environment schema and request planning are independent of release waiting.
type AppEnvironmentResource struct {
	managedResource[AppEnvironmentResourceModel, client.AppEnvironment]
}

type AppEnvironmentResourceModel struct {
	App                   types.String   `tfsdk:"app"`
	Name                  types.String   `tfsdk:"name"`
	Cluster               types.String   `tfsdk:"cluster"`
	DeletionProtection    types.Bool     `tfsdk:"deletion_protection"`
	DeploymentRevision    types.Int64    `tfsdk:"deployment_revision"`
	Web                   types.Object   `tfsdk:"web"`
	Secrets               types.List     `tfsdk:"secrets"`
	Domains               types.Set      `tfsdk:"domains"`
	Namespace             types.String   `tfsdk:"namespace"`
	Address               types.String   `tfsdk:"address"`
	Phase                 types.String   `tfsdk:"phase"`
	DeploymentPhase       types.String   `tfsdk:"deployment_phase"`
	DeploymentReason      types.String   `tfsdk:"deployment_reason"`
	RequestedRelease      types.String   `tfsdk:"requested_release"`
	ActiveRelease         types.String   `tfsdk:"active_release"`
	LastSuccessfulRelease types.String   `tfsdk:"last_successful_release"`
	RecoveryPhase         types.String   `tfsdk:"recovery_phase"`
	RecoveryRelease       types.String   `tfsdk:"recovery_release"`
	ETag                  types.String   `tfsdk:"etag"`
	Timeouts              timeouts.Value `tfsdk:"timeouts"`
}

var appQuantityTypes = map[string]attr.Type{"cpu": types.StringType, "memory": types.StringType}
var appResourcesTypes = map[string]attr.Type{"requests": types.ObjectType{AttrTypes: appQuantityTypes}, "limits": types.ObjectType{AttrTypes: appQuantityTypes}}
var appKeyRefTypes = map[string]attr.Type{"secret": types.StringType, "key": types.StringType}
var appEnvTypes = map[string]attr.Type{"name": types.StringType, "value": types.StringType, "secret_key_ref": types.ObjectType{AttrTypes: appKeyRefTypes}}
var appSecretTypes = map[string]attr.Type{"name": types.StringType, "path": types.StringType}
var appHTTPTypes = map[string]attr.Type{"path": types.StringType}
var appProbeTypes = map[string]attr.Type{"tcp": types.ObjectType{AttrTypes: map[string]attr.Type{}}, "http": types.ObjectType{AttrTypes: appHTTPTypes}}
var appHealthTypes = map[string]attr.Type{"readiness": types.ObjectType{AttrTypes: appProbeTypes}, "liveness": types.ObjectType{AttrTypes: appProbeTypes}}
var appWebTypes = map[string]attr.Type{
	"image": types.StringType, "port": types.Int64Type, "replicas": types.Int64Type,
	"resources": types.ObjectType{AttrTypes: appResourcesTypes},
	"command":   types.ListType{ElemType: types.StringType}, "args": types.ListType{ElemType: types.StringType},
	"env":           types.ListType{ElemType: types.ObjectType{AttrTypes: appEnvTypes}},
	"health_checks": types.ObjectType{AttrTypes: appHealthTypes},
}

func appDefaultQuantities(cpu, memory string) types.Object {
	return types.ObjectValueMust(appQuantityTypes, map[string]attr.Value{"cpu": types.StringValue(cpu), "memory": types.StringValue(memory)})
}

func appDefaultProbe() types.Object {
	return types.ObjectValueMust(appProbeTypes, map[string]attr.Value{"tcp": types.ObjectValueMust(map[string]attr.Type{}, map[string]attr.Value{}), "http": types.ObjectNull(appHTTPTypes)})
}

func (r *AppEnvironmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_environment"
}

func (r *AppEnvironmentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"app":                 schema.StringAttribute{Required: true, Description: "App identity. Reference `kupe_app.this.name` to order creation and deletion.", Validators: appNameValidators(32), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name":                schema.StringAttribute{Required: true, Description: "Environment name: an RFC 1123 label of at most 20 characters without `--`.", Validators: appNameValidators(20), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"cluster":             schema.StringAttribute{Required: true, Description: "Cluster that runs the environment. Changing it replaces the environment.", Validators: clusterRefValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"deletion_protection": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Set to false and apply before destroying the environment."},
		"deployment_revision": schema.Int64Attribute{Optional: true, Computed: true, Description: "Increase by one to retry the current deployment. An unchanged apply never retries.", Validators: []validator.Int64{int64validator.Between(1, 2147483647)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"web":                 appWebAttribute(),
		"secrets": schema.ListNestedAttribute{Optional: true, Computed: true, Default: listdefault.StaticValue(types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, []attr.Value{})), Description: "Vault paths referenced by the web workload. Write their values in the console Vault page.", Validators: []validator.List{listvalidator.SizeAtMost(20)}, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthAtMost(63), stringvalidator.RegexMatches(namespacePattern, "must be an RFC 1123 label")}, Description: "Name used by secret_key_ref."},
			"path": schema.StringAttribute{Required: true, Description: "Tenant Vault path.", Validators: []validator.String{stringvalidator.LengthBetween(1, 512)}},
		}}},
		"domains":  schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})), Description: "Complete set of exact lowercase custom hostnames. Domain changes do not deploy.", Validators: []validator.Set{setvalidator.SizeAtMost(5)}},
		"etag":     etagAttribute(),
		"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
	}
	for key, description := range map[string]string{
		"namespace": "Namespace inside the cluster. Destroying the environment deletes this namespace and everything in it.",
		"address":   "Generated HTTPS address.", "phase": "Current environment phase.",
		"deployment_phase": "Outcome of the requested release, independently of recovery.", "deployment_reason": "Reason for the requested release's outcome.",
		"requested_release": "Requested release name.", "active_release": "Release currently serving.", "last_successful_release": "Most recent successful release.",
		"recovery_phase": "Recovery outcome after a failed request.", "recovery_release": "Release used for recovery.",
	} {
		attrs[key] = schema.StringAttribute{Computed: true, Description: description, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	}
	resp.Schema = schema.Schema{Description: "Manages one Kupe app environment. Applying deploying settings requests a release. A failed first deployment remains in state without taint; a failed update records the accepted settings and reports an error. Increase deployment_revision by one to retry. Destroying the environment deletes its namespace and everything in it; the cluster is kept. Turn deletion protection off and apply before destroying. Do not independently target this namespace with a kupe_secret or binding.", Attributes: attrs}
}

func appWebAttribute() schema.SingleNestedAttribute {
	stringsList := func(description string) schema.ListAttribute {
		return schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: description, Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})), Validators: []validator.List{listvalidator.SizeAtMost(32)}}
	}
	quantity := func(cpu, memory string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{Optional: true, Computed: true, Default: objectdefault.StaticValue(appDefaultQuantities(cpu, memory)), Attributes: map[string]schema.Attribute{
			"cpu":    schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(cpu), Validators: cpuQuantityValidators(), Description: "CPU quantity."},
			"memory": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(memory), Validators: sizeQuantityValidators(), Description: "Memory quantity."},
		}}
	}
	resources := types.ObjectValueMust(appResourcesTypes, map[string]attr.Value{"requests": appDefaultQuantities("250m", "256Mi"), "limits": appDefaultQuantities("1", "512Mi")})
	health := types.ObjectValueMust(appHealthTypes, map[string]attr.Value{"readiness": appDefaultProbe(), "liveness": types.ObjectNull(appProbeTypes)})
	return schema.SingleNestedAttribute{Required: true, Description: "Complete runtime configuration. Images must be pinned by digest.", Attributes: map[string]schema.Attribute{
		"image":     schema.StringAttribute{Required: true, Validators: []validator.String{appImageDigest{}}, Description: "Image including a registry host and sha256 digest."},
		"port":      schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}, Description: "Container port."},
		"replicas":  schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1), Validators: []validator.Int64{int64validator.Between(1, 10)}, Description: "Replica count, 1 to 10."},
		"resources": schema.SingleNestedAttribute{Optional: true, Computed: true, Default: objectdefault.StaticValue(resources), Description: "Requests and limits.", Attributes: map[string]schema.Attribute{"requests": quantity("250m", "256Mi"), "limits": quantity("1", "512Mi")}},
		"command":   stringsList("Entrypoint, at most 32 strings of 1024 bytes each."),
		"args":      stringsList("Arguments, at most 32 strings of 1024 bytes each."),
		"env": schema.ListNestedAttribute{Optional: true, Computed: true, Default: listdefault.StaticValue(types.ListValueMust(types.ObjectType{AttrTypes: appEnvTypes}, []attr.Value{})), Description: "Ordered environment variables. Choose value or secret_key_ref per entry.", Validators: []validator.List{listvalidator.SizeAtMost(100)}, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":  schema.StringAttribute{Required: true, Description: "Environment variable name."},
			"value": schema.StringAttribute{Optional: true, Description: "Literal value. Use secret_key_ref for credentials."},
			"secret_key_ref": schema.SingleNestedAttribute{Optional: true, Description: "Reference to a declared secret, never a secret value.", Attributes: map[string]schema.Attribute{
				"secret": schema.StringAttribute{Required: true, Description: "Declared secret name."},
				"key":    schema.StringAttribute{Required: true, Validators: []validator.String{configMapKey{}}, Description: "Key in the Vault entry."},
			}},
		}}},
		"health_checks": schema.SingleNestedAttribute{Optional: true, Computed: true, Default: objectdefault.StaticValue(health), Description: "Readiness and optional liveness probes.", Attributes: map[string]schema.Attribute{
			"readiness": appProbeAttribute(true), "liveness": appProbeAttribute(false),
		}},
	}}
}

func appProbeAttribute(readiness bool) schema.SingleNestedAttribute {
	attribute := schema.SingleNestedAttribute{Optional: true, Description: "Choose exactly one of tcp or http.", Attributes: map[string]schema.Attribute{
		"tcp":  schema.SingleNestedAttribute{Optional: true, Description: "TCP probe on the web port.", Attributes: map[string]schema.Attribute{}},
		"http": schema.SingleNestedAttribute{Optional: true, Description: "HTTP probe on the web port.", Attributes: map[string]schema.Attribute{"path": schema.StringAttribute{Required: true, Description: "HTTP path beginning with /."}}},
	}}
	if readiness {
		attribute.Computed = true
		attribute.Default = objectdefault.StaticValue(appDefaultProbe())
	}
	return attribute
}

func (r *AppEnvironmentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var planned, prior types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("deployment_revision"), &planned)...)
	if req.State.Raw.IsNull() {
		if !planned.IsNull() && !planned.IsUnknown() && planned.ValueInt64() != 1 {
			resp.Diagnostics.AddAttributeError(path.Root("deployment_revision"), "Invalid deployment revision", "A new environment starts at deployment_revision = 1.")
		}
		return
	}
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("deployment_revision"), &prior)...)
	if resp.Diagnostics.HasError() || planned.IsUnknown() || planned.IsNull() || prior.IsUnknown() || prior.IsNull() {
		return
	}
	var planModel, priorModel AppEnvironmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planModel)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &priorModel)...)
	if !resp.Diagnostics.HasError() && appEnvironmentDeployingChange(&planModel, &priorModel) {
		for _, name := range []string{"phase", "deployment_phase", "deployment_reason", "requested_release", "active_release", "last_successful_release", "recovery_phase", "recovery_release"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
		}
	}
	if planned.ValueInt64() != prior.ValueInt64() && planned.ValueInt64() != prior.ValueInt64()+1 {
		resp.Diagnostics.AddAttributeError(path.Root("deployment_revision"), "Invalid deployment revision", "deployment_revision can only be increased by one per apply")
	}
}
