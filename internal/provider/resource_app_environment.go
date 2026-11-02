package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

var (
	_ resource.Resource                   = &AppEnvironmentResource{}
	_ resource.ResourceWithConfigure      = &AppEnvironmentResource{}
	_ resource.ResourceWithImportState    = &AppEnvironmentResource{}
	_ resource.ResourceWithModifyPlan     = &AppEnvironmentResource{}
	_ resource.ResourceWithValidateConfig = &AppEnvironmentResource{}
)

var appDegradedGrace = 2 * time.Minute

// Approved specifically for provider69; a later non-deploying edit can also
// make the original release unidentifiable. Never label that Superseded.
const appReleaseUnidentified = "the environment changed before its release could be identified; check its current state before deploying again"

// Unlike changeRecordedNotice this remains true after a concurrent foreign
// settings change. Refresh may expose that drift and a user may apply it again.
const appChangeRecordedNotice = "The request was accepted and is recorded in Terraform state. The provider will not send it again during this apply. An unchanged configuration plans no change while the accepted settings remain current. A later refresh may show another client's changes; review the next plan before applying."

func NewAppEnvironmentResource() resource.Resource {
	return &AppEnvironmentResource{managedResource[AppEnvironmentResourceModel, client.AppEnvironment]{kind: &appEnvironmentKind}}
}

var appEnvironmentKind = serviceKind[AppEnvironmentResourceModel, client.AppEnvironment]{
	noun: "app environment",
	name: func(m *AppEnvironmentResourceModel) string { return m.App.ValueString() + "/" + m.Name.ValueString() },
	get: func(ctx context.Context, c *client.Client, m *AppEnvironmentResourceModel) (*client.AppEnvironment, string, error) {
		return c.GetAppEnvironment(ctx, m.App.ValueString(), m.Name.ValueString())
	},
	del: func(ctx context.Context, c *client.Client, m *AppEnvironmentResourceModel) error {
		return c.DeleteAppEnvironment(ctx, m.App.ValueString(), m.Name.ValueString(), m.ETag.ValueString())
	},
	mapToState:      mapAppEnvironmentToState,
	timeoutsOf:      func(m *AppEnvironmentResourceModel) timeouts.Value { return m.Timeouts },
	defaults:        operationTimeouts{create: 30 * time.Minute, update: 20 * time.Minute, delete: 20 * time.Minute},
	settingsChanged: appEnvironmentSettingsChanged,
	deleteRefused: func(m *AppEnvironmentResourceModel, err error) (string, string, bool) {
		if client.IsDeletionProtected(err) {
			return "app environment has deletion protection enabled", fmt.Sprintf("Environment %q was kept. Set `deletion_protection = false`, run `terraform apply`, then destroy it. Destroying an environment deletes its namespace and everything in it. The cluster and Vault entries are kept.", m.App.ValueString()+"/"+m.Name.ValueString()), true
		}
		if client.ErrorCode(err) == "namespace_in_use" {
			return "app environment namespace is in use", apiErrorDetail(err) + "\n\nRemove independent secret deliveries and service bindings targeting the environment's namespace, then destroy it again.", true
		}
		return "", "", false
	},
	stillDeleting: func(m *AppEnvironmentResourceModel) string {
		return fmt.Sprintf("DELETE was accepted but environment %q is still terminating after the delete timeout. Terraform has removed it from state; cleanup continues in the background. Its name stays reserved until cleanup finishes. The cluster and Vault entries are kept.", m.App.ValueString()+"/"+m.Name.ValueString())
	},
}

func (r *AppEnvironmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || len(parts[0]) > 32 || len(parts[1]) > 20 || !clusterRefPattern.MatchString(parts[0]) || !clusterRefPattern.MatchString(parts[1]) {
		resp.Diagnostics.AddError("Invalid import ID", "Use <app>/<environment>, for example shop/production.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[1])...)
}

func (r *AppEnvironmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m AppEnvironmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	env, etag, err := r.client.GetAppEnvironment(ctx, m.App.ValueString(), m.Name.ValueString())
	if client.ErrorCode(err) == client.CodeAppsUnavailable {
		resp.Diagnostics.AddWarning("apps unavailable", apiErrorDetail(err))
		return
	}
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("failed to read app environment", apiErrorDetail(err))
		return
	}
	mapAppEnvironmentToState(env, etag, &m)
	if resp.Private != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, "app_environment_uid", []byte(strconv.Quote(env.UID)))...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *AppEnvironmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AppEnvironmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	web, secrets, domains, diags := appEnvironmentSettings(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	timeout, diags := plan.Timeouts.Create(ctx, appEnvironmentKind.defaults.create)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	protected := plan.DeletionProtection.ValueBool()
	env, etag, err := r.client.CreateAppEnvironment(ctx, plan.App.ValueString(), client.CreateAppEnvironmentRequest{Environment: plan.Name.ValueString(), Cluster: plan.Cluster.ValueString(), Web: web, Secrets: secrets, Domains: domains, DeletionProtection: &protected})
	if err != nil {
		resp.Diagnostics.AddError("failed to create app environment", apiErrorDetail(err))
		return
	}
	mapAppEnvironmentToState(env, etag, &plan)
	if resp.Private != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, "app_environment_uid", []byte(strconv.Quote(env.UID)))...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A wait outcome after a confirmed POST is a warning on Create: a failed
	// first deployment must never taint a resource and schedule its deletion.
	r.waitAppEnvironment(ctx, timeout, "create", env, &plan, true, &resp.State, &resp.Diagnostics)
}

func (r *AppEnvironmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior AppEnvironmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch, changed, diags := appEnvironmentPatch(ctx, &plan, &prior)
	resp.Diagnostics.Append(diags...)
	timeout, diags := plan.Timeouts.Update(ctx, appEnvironmentKind.defaults.update)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	retry := !plan.DeploymentRevision.Equal(prior.DeploymentRevision)
	deploying := appEnvironmentDeployingChange(&plan, &prior)
	var accepted *client.AppEnvironment
	etag := prior.ETag.ValueString()
	baseline, _, err := r.client.GetAppEnvironment(ctx, plan.App.ValueString(), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to read app environment before update", apiErrorDetail(err))
		return
	}
	uidData, uidDiags := req.Private.GetKey(ctx, "app_environment_uid")
	resp.Diagnostics.Append(uidDiags...)
	var priorUID string
	if len(uidData) > 0 {
		if err := json.Unmarshal(uidData, &priorUID); err != nil {
			resp.Diagnostics.AddError("invalid environment identity in private state", "Refresh the environment before applying again.")
			return
		}
	}
	if priorUID != "" && baseline.UID != priorUID {
		resp.Diagnostics.AddError("environment was replaced", "The environment identity changed since the plan. Run plan again before applying.")
		return
	}
	if fields := appEnvironmentSettingsChanged(&prior, baseline); len(fields) > 0 {
		resp.Diagnostics.AddError("environment changed outside this apply", "The environment's settings changed since the plan. Run plan again before applying.")
		return
	}
	if resp.Private != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, "app_environment_uid", []byte(strconv.Quote(baseline.UID)))...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if changed {
		var err error
		accepted, etag, err = r.updateWithRetry(ctx, &prior, etag, func(ctx context.Context, etag string) (*client.AppEnvironment, string, error) {
			current, _, err := r.client.GetAppEnvironment(ctx, plan.App.ValueString(), plan.Name.ValueString())
			if err != nil {
				return nil, "", err
			}
			if current.UID != baseline.UID {
				return nil, "", fmt.Errorf("the environment was replaced before PATCH")
			}
			return r.client.PatchAppEnvironment(ctx, plan.App.ValueString(), plan.Name.ValueString(), etag, patch)
		})
		if err != nil {
			resp.Diagnostics.AddError("failed to update app environment", apiErrorDetail(err))
			return
		}
		// Record the PATCH before retry. If retry fails, accepted settings
		// remain tracked but its unaccepted revision is not marked done.
		mapAppEnvironmentToState(accepted, etag, &plan)
		if !deploying {
			preserveAppPlannedStatus(&plan, &prior)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if retry {
		var revision *client.AppRetryResponse
		var err error
		revision, etag, err = r.retryAppEnvironment(ctx, &plan, &prior, accepted, baseline.UID, etag)
		if err != nil {
			resp.Diagnostics.AddError("failed to retry app environment", apiErrorDetail(err))
			return
		}
		if accepted == nil {
			// Baseline was read before the conditional retry and checked
			// against state. Preserve its UID and stable namespace/address;
			// a GET after retry could already belong to a newer request.
			snapshot := *baseline
			accepted = &snapshot
		}
		accepted.Generation, accepted.DeploymentRevision = revision.Generation, revision.DeploymentRevision
		mapAppEnvironmentToState(accepted, etag, &plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if accepted == nil {
		var err error
		accepted, etag, err = r.client.GetAppEnvironment(ctx, plan.App.ValueString(), plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("failed to read app environment", apiErrorDetail(err))
			return
		}
		plannedStatus := plan
		mapAppEnvironmentToState(accepted, etag, &plan)
		preserveAppPlannedStatus(&plan, &plannedStatus)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	r.waitAppEnvironment(ctx, timeout, "update", accepted, &plan, deploying, &resp.State, &resp.Diagnostics)
}

// retryAppEnvironment retries one stale If-Match only if every setting still
// matches the accepted PATCH (or prior state for retry-only). No POST transport
// retry is enabled, so an uncertain accepted retry is never submitted twice.
func (r *AppEnvironmentResource) retryAppEnvironment(ctx context.Context, plan, prior *AppEnvironmentResourceModel, patched *client.AppEnvironment, uid, etag string) (*client.AppRetryResponse, string, error) {
	result, next, err := r.client.RetryAppEnvironment(ctx, plan.App.ValueString(), plan.Name.ValueString(), etag)
	if !client.IsPreconditionFailed(err) {
		return result, next, err
	}
	current, fresh, err := r.client.GetAppEnvironment(ctx, plan.App.ValueString(), plan.Name.ValueString())
	if err != nil {
		return nil, "", fmt.Errorf("reading environment after stale retry: %w", err)
	}
	if current.UID != uid {
		return nil, "", fmt.Errorf("the environment was replaced before retry")
	}
	expected := prior
	if patched != nil {
		acceptedPlan := *plan
		mapAppEnvironmentToState(patched, etag, &acceptedPlan)
		expected = &acceptedPlan
	}
	if fields := appEnvironmentSettingsChanged(expected, current); len(fields) > 0 {
		return nil, "", &concurrentChangeError{fields: fields}
	}
	return r.client.RetryAppEnvironment(ctx, plan.App.ValueString(), plan.Name.ValueString(), fresh)
}

func appEnvironmentSettingsChanged(prior *AppEnvironmentResourceModel, current *client.AppEnvironment) []string {
	var mapped AppEnvironmentResourceModel
	mapAppEnvironmentToState(current, "", &mapped)
	var fields []string
	for _, item := range []struct {
		name  string
		equal bool
	}{
		{"app", prior.App.Equal(mapped.App)}, {"name", prior.Name.Equal(mapped.Name)}, {"cluster", prior.Cluster.Equal(mapped.Cluster)},
		{"web", prior.Web.Equal(mapped.Web)}, {"secrets", prior.Secrets.Equal(mapped.Secrets)}, {"domains", prior.Domains.Equal(mapped.Domains)},
		{"deletion_protection", prior.DeletionProtection.Equal(mapped.DeletionProtection)}, {"deployment_revision", prior.DeploymentRevision.Equal(mapped.DeploymentRevision)},
	} {
		if !item.equal {
			fields = append(fields, item.name)
		}
	}
	return fields
}

var errAppWait = errors.New("app deployment wait ended")

func (r *AppEnvironmentResource) waitAppEnvironment(ctx context.Context, timeout time.Duration, op string, accepted *client.AppEnvironment, m *AppEnvironmentResourceModel, deploying bool, state *tfsdk.State, diags *diag.Diagnostics) {
	acceptedModel := *m
	var pinned string
	var degradedSince time.Time
	last := accepted
	detail := ""
	err := pollUntil(ctx, timeout, func(ctx context.Context) (bool, error) {
		current, etag, err := r.client.GetAppEnvironment(ctx, accepted.App, accepted.Environment)
		if err != nil {
			return false, err
		}
		last = current
		// Status observations never silently adopt another writer's config.
		mapAppEnvironmentToState(current, etag, m)
		m.App, m.Name, m.Cluster = acceptedModel.App, acceptedModel.Name, acceptedModel.Cluster
		m.Web, m.Secrets, m.Domains = acceptedModel.Web, acceptedModel.Secrets, acceptedModel.Domains
		m.DeletionProtection, m.DeploymentRevision = acceptedModel.DeletionProtection, acceptedModel.DeploymentRevision
		if !deploying {
			preserveAppPlannedStatus(m, &acceptedModel)
		}
		diags.Append(state.Set(ctx, m)...)
		if diags.HasError() {
			detail = "The accepted settings could not be recorded in state."
			return true, errAppWait
		}
		if current.UID != accepted.UID {
			detail = "The environment was replaced while waiting; the original request's outcome cannot verify the replacement."
			return true, errAppWait
		}
		if current.Status != nil && current.Status.Phase == phaseTerminating {
			detail = "The environment is being deleted, so the deployment wait cannot complete."
			return true, errAppWait
		}
		if !deploying {
			if len(appEnvironmentSettingsChanged(&acceptedModel, current)) > 0 {
				detail = "The environment settings changed after this request was accepted; review its current state before applying again."
				return true, errAppWait
			}
			return current.Status != nil && current.Status.ObservedGeneration >= accepted.Generation, nil
		}
		if pinned == "" && current.Generation > accepted.Generation {
			detail = appReleaseUnidentified
			return true, errAppWait
		}
		if current.Status == nil || current.Status.ObservedGeneration < accepted.Generation {
			return false, nil
		}
		releases, err := r.client.ListAppReleases(ctx, accepted.App, accepted.Environment)
		if err != nil {
			return false, err
		}
		if pinned == "" {
			if current.Generation != accepted.Generation || current.Status.RequestedRelease == nil || current.Status.Deployment == nil || current.Status.Deployment.Release == nil || current.Status.Deployment.Release.Name != current.Status.RequestedRelease.Name {
				return false, nil
			}
			for _, release := range releases {
				if release.Name == current.Status.RequestedRelease.Name && appReleaseMatches(accepted, &release) {
					pinned = release.Name
					break
				}
			}
		}
		if pinned == "" {
			return false, nil
		}
		for _, release := range releases {
			if release.Name != pinned {
				continue
			}
			switch release.Status.Phase {
			case "Failed", "Superseded", "Succeeded":
				verified, _, err := r.client.GetAppEnvironment(ctx, accepted.App, accepted.Environment)
				if err != nil {
					return false, err
				}
				if verified.UID != accepted.UID {
					detail = "The environment was replaced while its release history was being read; the old release does not verify the replacement."
					return true, errAppWait
				}
				last = verified
				if release.Status.Phase != "Succeeded" {
					detail = appReleaseFailureDetail(&release, verified)
					return true, errAppWait
				}
				// A completed pinned release stays a success if a later
				// request starts. An unhealthy current serving workload is
				// ridden out for the existing two-minute cluster grace.
				if verified.Status != nil && verified.Status.RequestedRelease != nil && verified.Status.RequestedRelease.Name == pinned {
					if verified.Status.Phase == "Degraded" {
						if degradedSince.IsZero() {
							degradedSince = time.Now()
						}
						if time.Since(degradedSince) >= appDegradedGrace {
							detail = fmt.Sprintf("Release %q succeeded, but the environment remained Degraded for two minutes.", pinned)
							return true, errAppWait
						}
						return false, nil
					}
					degradedSince = time.Time{}
					return verified.Status.Phase == "Running" && verified.Status.Deployment != nil && verified.Status.Deployment.Release != nil && verified.Status.Deployment.Release.Name == pinned && verified.Status.Deployment.Phase == "Succeeded", nil
				}
				return true, nil
			}
		}
		return false, nil
	})
	if err == nil {
		return
	}
	if detail == "" {
		if isTerminalAPIError(err) {
			detail = apiErrorDetail(err)
		} else {
			detail = fmt.Sprintf("The %s timeout ended before the requested release completed. Current requested release: %q. %s", op, appRefName(last), appEnvironmentOutcome(last))
		}
	}
	detail += "\n\n" + appChangeRecordedNotice + "\n\nTo request another deployment, increase `deployment_revision` by one and apply."
	if op == "create" {
		diags.AddWarning("app deployment did not complete", detail)
	} else {
		diags.AddError("app deployment did not complete", detail)
	}
}

func appRefName(env *client.AppEnvironment) string {
	if env.Status == nil || env.Status.RequestedRelease == nil {
		return ""
	}
	return env.Status.RequestedRelease.Name
}

func appEnvironmentOutcome(env *client.AppEnvironment) string {
	if env.Status == nil {
		return "No deployment status is available."
	}
	message := ""
	if env.Status.Deployment != nil {
		message = fmt.Sprintf("Deployment phase %q, reason %q.", env.Status.Deployment.Phase, env.Status.Deployment.Reason)
	}
	if env.Status.Recovery != nil {
		message += fmt.Sprintf(" Recovery phase %q, release %q.", env.Status.Recovery.Phase, releaseRefString(env.Status.Recovery.Release))
	}
	return message
}

func releaseRefString(ref *client.AppReleaseRef) string {
	if ref == nil {
		return ""
	}
	return ref.Name
}

func appReleaseFailureDetail(release *client.AppRelease, current *client.AppEnvironment) string {
	detail := fmt.Sprintf("Requested release %q ended %s, reason %q.", release.Name, release.Status.Phase, release.Status.Reason)
	if release.Status.Recovery != nil {
		detail += fmt.Sprintf(" Recovery phase %q, release %q.", release.Status.Recovery.Phase, releaseRefString(release.Status.Recovery.Release))
	}
	if current.Status != nil && current.Status.ActiveRelease != nil {
		detail += fmt.Sprintf(" Release %q is now serving.", current.Status.ActiveRelease.Name)
	}
	if release.Status.Phase == "Superseded" {
		if name := appRefName(current); name != "" && name != release.Name {
			detail += fmt.Sprintf(" The current requested release is %q.", name)
		} else {
			detail += " A newer release name is unavailable."
		}
	}
	return detail
}

func appReleaseMatches(accepted *client.AppEnvironment, release *client.AppRelease) bool {
	if accepted.DeploymentRevision != release.DeploymentRevision || accepted.Web == nil {
		return false
	}
	expectedWeb, releaseWeb := *accepted.Web, release.Runtime.AppWeb
	normaliseAppQuantities(&expectedWeb.Resources)
	normaliseAppQuantities(&releaseWeb.Resources)
	if !appWebToObject(&expectedWeb).Equal(appWebToObject(&releaseWeb)) {
		return false
	}
	referenced := map[string]map[string]bool{}
	for _, env := range accepted.Web.Env {
		if env.ValueFrom != nil {
			ref := env.ValueFrom.SecretKeyRef
			if referenced[ref.Secret] == nil {
				referenced[ref.Secret] = map[string]bool{}
			}
			referenced[ref.Secret][ref.Key] = true
		}
	}
	paths := map[string]string{}
	for _, secret := range accepted.Secrets {
		if referenced[secret.Name] != nil {
			paths[secret.Name] = secret.Path
		}
	}
	if len(paths) != len(release.Runtime.Secrets) {
		return false
	}
	for _, secret := range release.Runtime.Secrets {
		if paths[secret.Name] != secret.Path {
			return false
		}
		keys := map[string]bool{}
		for _, key := range secret.Keys {
			keys[key] = true
		}
		if !reflect.DeepEqual(keys, referenced[secret.Name]) {
			return false
		}
	}
	return true
}

// Settings-only applies do not claim another deployment's status transition.
// Keep known planned values consistent; the next Read refreshes current status.
func preserveAppPlannedStatus(m, planned *AppEnvironmentResourceModel) {
	if !planned.Phase.IsUnknown() {
		m.Phase = planned.Phase
	}
	if !planned.DeploymentPhase.IsUnknown() {
		m.DeploymentPhase = planned.DeploymentPhase
	}
	if !planned.DeploymentReason.IsUnknown() {
		m.DeploymentReason = planned.DeploymentReason
	}
	if !planned.RequestedRelease.IsUnknown() {
		m.RequestedRelease = planned.RequestedRelease
	}
	if !planned.ActiveRelease.IsUnknown() {
		m.ActiveRelease = planned.ActiveRelease
	}
	if !planned.LastSuccessfulRelease.IsUnknown() {
		m.LastSuccessfulRelease = planned.LastSuccessfulRelease
	}
	if !planned.RecoveryPhase.IsUnknown() {
		m.RecoveryPhase = planned.RecoveryPhase
	}
	if !planned.RecoveryRelease.IsUnknown() {
		m.RecoveryRelease = planned.RecoveryRelease
	}
}

// Releases use canonical Kubernetes quantity spellings. Compare their exact
// values so an accepted 0.5 CPU request matches the same 500m runtime.
func normaliseAppQuantities(resources *client.AppResources) {
	for _, quantities := range []*client.AppQuantities{&resources.Requests, &resources.Limits} {
		if cpu, ok := parseAppCPUQuantity(quantities.CPU); ok {
			quantities.CPU = cpu.RatString()
		}
		if memory, ok := parseSizeQuantity(quantities.Memory); ok {
			quantities.Memory = memory.RatString()
		}
	}
}
