package provider

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

type appWebModel struct {
	Image        types.String `tfsdk:"image"`
	Port         types.Int64  `tfsdk:"port"`
	Replicas     types.Int64  `tfsdk:"replicas"`
	Resources    types.Object `tfsdk:"resources"`
	Command      types.List   `tfsdk:"command"`
	Args         types.List   `tfsdk:"args"`
	Env          types.List   `tfsdk:"env"`
	HealthChecks types.Object `tfsdk:"health_checks"`
}

type appEnvModel struct {
	Name         types.String `tfsdk:"name"`
	Value        types.String `tfsdk:"value"`
	SecretKeyRef types.Object `tfsdk:"secret_key_ref"`
}

type appSecretModel struct {
	Name types.String `tfsdk:"name"`
	Path types.String `tfsdk:"path"`
}

// appEnvironmentSettings converts a fully known apply plan. Unknown values
// remain framework diagnostics, never zero-valued API writes.
func appEnvironmentSettings(ctx context.Context, m *AppEnvironmentResourceModel) (*client.AppWeb, []client.AppSecret, []string, diag.Diagnostics) {
	var diags diag.Diagnostics
	rejectNullAppListEntries(m.Secrets, path.Root("secrets"), &diags)
	var web appWebModel
	diags.Append(m.Web.As(ctx, &web, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, nil, nil, diags
	}
	rejectNullAppListEntries(web.Env, path.Root("web").AtName("env"), &diags)
	if diags.HasError() {
		return nil, nil, nil, diags
	}
	// #nosec G115 -- The schema validates port in 1..65535 and replicas in 1..10 before apply.
	out := &client.AppWeb{Image: web.Image.ValueString(), Port: int32(web.Port.ValueInt64()), Replicas: int32(web.Replicas.ValueInt64()), Command: []string{}, Args: []string{}, Env: []client.AppEnvVar{}}
	diags.Append(web.Command.ElementsAs(ctx, &out.Command, false)...)
	diags.Append(web.Args.ElementsAs(ctx, &out.Args, false)...)
	if diags.HasError() {
		return nil, nil, nil, diags
	}
	resources := web.Resources.Attributes()
	out.Resources.Requests = appQuantitiesFromObject(resources["requests"].(types.Object))
	out.Resources.Limits = appQuantitiesFromObject(resources["limits"].(types.Object))
	var env []appEnvModel
	diags.Append(web.Env.ElementsAs(ctx, &env, false)...)
	if diags.HasError() {
		return nil, nil, nil, diags
	}
	for _, entry := range env {
		value := client.AppEnvVar{Name: entry.Name.ValueString()}
		if !entry.Value.IsNull() {
			literal := entry.Value.ValueString()
			value.Value = &literal
		}
		if !entry.SecretKeyRef.IsNull() {
			ref := entry.SecretKeyRef.Attributes()
			value.ValueFrom = &client.AppValueFrom{SecretKeyRef: client.AppSecretKeyRef{Secret: ref["secret"].(types.String).ValueString(), Key: ref["key"].(types.String).ValueString()}}
		}
		out.Env = append(out.Env, value)
	}
	health := web.HealthChecks.Attributes()
	out.HealthChecks = client.AppHealthChecks{Readiness: appProbeFromObject(health["readiness"].(types.Object)), Liveness: appProbeFromObject(health["liveness"].(types.Object))}
	var secrets []appSecretModel
	diags.Append(m.Secrets.ElementsAs(ctx, &secrets, false)...)
	if diags.HasError() {
		return nil, nil, nil, diags
	}
	secretRefs := make([]client.AppSecret, 0, len(secrets))
	for _, secret := range secrets {
		secretRefs = append(secretRefs, client.AppSecret{Name: secret.Name.ValueString(), Path: secret.Path.ValueString()})
	}
	domains := []string{}
	diags.Append(m.Domains.ElementsAs(ctx, &domains, false)...)
	sort.Strings(domains)
	return out, secretRefs, domains, diags
}

func appQuantitiesFromObject(object types.Object) client.AppQuantities {
	values := object.Attributes()
	return client.AppQuantities{CPU: values["cpu"].(types.String).ValueString(), Memory: values["memory"].(types.String).ValueString()}
}

func appProbeFromObject(object types.Object) *client.AppProbe {
	if object.IsNull() {
		return nil
	}
	values := object.Attributes()
	probe := &client.AppProbe{}
	if !values["tcp"].IsNull() {
		probe.TCP = &client.AppTCPProbe{}
	}
	if http := values["http"].(types.Object); !http.IsNull() {
		probe.HTTP = &client.AppHTTPProbe{Path: http.Attributes()["path"].(types.String).ValueString()}
	}
	return probe
}

func appProbeToObject(probe *client.AppProbe) types.Object {
	if probe == nil {
		return types.ObjectNull(appProbeTypes)
	}
	tcp, http := types.ObjectNull(map[string]attr.Type{}), types.ObjectNull(appHTTPTypes)
	if probe.TCP != nil {
		tcp = types.ObjectValueMust(map[string]attr.Type{}, map[string]attr.Value{})
	}
	if probe.HTTP != nil {
		http = types.ObjectValueMust(appHTTPTypes, map[string]attr.Value{"path": types.StringValue(probe.HTTP.Path)})
	}
	return types.ObjectValueMust(appProbeTypes, map[string]attr.Value{"tcp": tcp, "http": http})
}

func appWebToObject(web *client.AppWeb) types.Object {
	if web == nil {
		return types.ObjectNull(appWebTypes)
	}
	stringsList := func(values []string) types.List {
		out := make([]attr.Value, 0, len(values))
		for _, value := range values {
			out = append(out, types.StringValue(value))
		}
		return types.ListValueMust(types.StringType, out)
	}
	env := make([]attr.Value, 0, len(web.Env))
	for _, value := range web.Env {
		literal, ref := types.StringNull(), types.ObjectNull(appKeyRefTypes)
		if value.Value != nil {
			literal = types.StringValue(*value.Value)
		}
		if value.ValueFrom != nil {
			ref = types.ObjectValueMust(appKeyRefTypes, map[string]attr.Value{"secret": types.StringValue(value.ValueFrom.SecretKeyRef.Secret), "key": types.StringValue(value.ValueFrom.SecretKeyRef.Key)})
		}
		env = append(env, types.ObjectValueMust(appEnvTypes, map[string]attr.Value{"name": types.StringValue(value.Name), "value": literal, "secret_key_ref": ref}))
	}
	return types.ObjectValueMust(appWebTypes, map[string]attr.Value{
		"image": types.StringValue(web.Image), "port": types.Int64Value(int64(web.Port)), "replicas": types.Int64Value(int64(web.Replicas)),
		"resources": types.ObjectValueMust(appResourcesTypes, map[string]attr.Value{"requests": appDefaultQuantities(web.Resources.Requests.CPU, web.Resources.Requests.Memory), "limits": appDefaultQuantities(web.Resources.Limits.CPU, web.Resources.Limits.Memory)}),
		"command":   stringsList(web.Command), "args": stringsList(web.Args), "env": types.ListValueMust(types.ObjectType{AttrTypes: appEnvTypes}, env),
		"health_checks": types.ObjectValueMust(appHealthTypes, map[string]attr.Value{"readiness": appProbeToObject(web.HealthChecks.Readiness), "liveness": appProbeToObject(web.HealthChecks.Liveness)}),
	})
}

func mapAppEnvironmentToState(env *client.AppEnvironment, etag string, m *AppEnvironmentResourceModel) {
	m.App, m.Name, m.Cluster = types.StringValue(env.App), types.StringValue(env.Environment), types.StringValue(env.Cluster)
	m.DeletionProtection, m.DeploymentRevision = types.BoolValue(env.DeletionProtection), types.Int64Value(env.DeploymentRevision)
	m.Web = appWebToObject(env.Web)
	secrets := make([]attr.Value, 0, len(env.Secrets))
	for _, secret := range env.Secrets {
		secrets = append(secrets, types.ObjectValueMust(appSecretTypes, map[string]attr.Value{"name": types.StringValue(secret.Name), "path": types.StringValue(secret.Path)}))
	}
	m.Secrets = types.ListValueMust(types.ObjectType{AttrTypes: appSecretTypes}, secrets)
	domains := make([]attr.Value, 0, len(env.Domains))
	for _, domain := range env.Domains {
		domains = append(domains, types.StringValue(domain))
	}
	m.Domains = types.SetValueMust(types.StringType, domains)
	m.ETag = types.StringValue(etag)
	m.Namespace, m.Address, m.Phase = types.StringNull(), types.StringNull(), types.StringNull()
	m.DeploymentPhase, m.DeploymentReason = types.StringNull(), types.StringNull()
	m.RequestedRelease, m.ActiveRelease, m.LastSuccessfulRelease = types.StringNull(), types.StringNull(), types.StringNull()
	m.RecoveryPhase, m.RecoveryRelease = types.StringNull(), types.StringNull()
	if env.Status == nil {
		return
	}
	s := env.Status
	m.Namespace, m.Address, m.Phase = stringOrNull(s.Namespace), stringOrNull(s.Address), stringOrNull(s.Phase)
	m.RequestedRelease, m.ActiveRelease, m.LastSuccessfulRelease = appReleaseName(s.RequestedRelease), appReleaseName(s.ActiveRelease), appReleaseName(s.LastSuccessfulRelease)
	if s.Deployment != nil {
		m.DeploymentPhase, m.DeploymentReason = stringOrNull(s.Deployment.Phase), stringOrNull(s.Deployment.Reason)
	}
	if s.Recovery != nil {
		m.RecoveryPhase, m.RecoveryRelease = stringOrNull(s.Recovery.Phase), appReleaseName(s.Recovery.Release)
	}
}

func appReleaseName(ref *client.AppReleaseRef) types.String {
	if ref == nil {
		return types.StringNull()
	}
	return stringOrNull(ref.Name)
}

// appEnvironmentPatch sends the complete plan's lists with any settings patch.
// Even a protection-only patch carries all lists, so list removals never become
// omissions during a combined update. This does not change deploymentRevision.
func appEnvironmentPatch(ctx context.Context, plan, prior *AppEnvironmentResourceModel) (client.PatchAppEnvironmentRequest, bool, diag.Diagnostics) {
	web, secrets, domains, diags := appEnvironmentSettings(ctx, plan)
	changed := !plan.Web.Equal(prior.Web) || !plan.Secrets.Equal(prior.Secrets) || !plan.Domains.Equal(prior.Domains) || !plan.DeletionProtection.Equal(prior.DeletionProtection)
	if diags.HasError() || !changed {
		return client.PatchAppEnvironmentRequest{}, false, diags
	}
	protected := plan.DeletionProtection.ValueBool()
	if plan.Web.Equal(prior.Web) && plan.Secrets.Equal(prior.Secrets) && plan.Domains.Equal(prior.Domains) {
		// The API serves protection-only cleanup while Apps are off.
		return client.PatchAppEnvironmentRequest{DeletionProtection: &protected}, true, diags
	}
	return client.PatchAppEnvironmentRequest{Web: web, Secrets: &secrets, Domains: &domains, DeletionProtection: &protected}, true, diags
}

// Only secret entries referenced by web.env contribute to a release. Changing
// an unused Vault path is a settings update, just like a domain change.
func appEnvironmentDeployingChange(plan, prior *AppEnvironmentResourceModel) bool {
	if !plan.Web.Equal(prior.Web) || !plan.DeploymentRevision.Equal(prior.DeploymentRevision) {
		return true
	}
	if plan.Secrets.Equal(prior.Secrets) {
		return false
	}
	if plan.Web.IsNull() || plan.Web.IsUnknown() || plan.Secrets.IsUnknown() || prior.Secrets.IsUnknown() {
		return true
	}
	env := plan.Web.Attributes()["env"].(types.List)
	if env.IsUnknown() {
		return true
	}
	referenced := map[string]bool{}
	for _, item := range env.Elements() {
		if item.IsNull() || item.IsUnknown() {
			return true
		}
		ref := item.(types.Object).Attributes()["secret_key_ref"].(types.Object)
		if ref.IsUnknown() {
			return true
		}
		if !ref.IsNull() {
			name := ref.Attributes()["secret"].(types.String)
			if name.IsUnknown() {
				return true
			}
			referenced[name.ValueString()] = true
		}
	}
	paths := func(list types.List) map[string]string {
		out := map[string]string{}
		for _, item := range list.Elements() {
			if item.IsNull() || item.IsUnknown() {
				return nil
			}
			attrs := item.(types.Object).Attributes()
			name, path := attrs["name"].(types.String), attrs["path"].(types.String)
			if name.IsUnknown() || path.IsUnknown() {
				return nil
			}
			out[name.ValueString()] = path.ValueString()
		}
		return out
	}
	a, b := paths(plan.Secrets), paths(prior.Secrets)
	if a == nil || b == nil {
		return true
	}
	for name := range referenced {
		if a[name] != b[name] {
			return true
		}
	}
	return false
}
