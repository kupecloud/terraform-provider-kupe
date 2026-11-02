package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// These call the real resource wait with an HTTP client and real framework
// state. Acceptance tests separately verify CLI taint and next-plan semantics.
func TestAppEnvironmentWaitReleaseIdentityAndFailures(t *testing.T) {
	shrinkPollInterval(t)
	for _, tc := range []struct {
		name, phase, op string
		mutate          func(*mockKupeAPI, *client.AppEnvironment)
		want            string
		warning         bool
	}{
		{"failed create warns", "Failed", "create", nil, "Requested release", true},
		{"failed update errors", "Failed", "update", nil, "Requested release", false},
		{"recovered request remains failed", "Failed", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			releases := m.appReleases["shop/production"]
			releases[0].Status.Recovery = &client.AppReleaseRecovery{Phase: "Succeeded", Release: &client.AppReleaseRef{Name: "previous"}}
			m.appReleases["shop/production"] = releases
			e.Status.ActiveRelease = &client.AppReleaseRef{Name: "previous"}
		}, "now serving", false},
		{"prepin generation is unverified", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(e *client.AppEnvironment, _ *[]client.AppRelease) {
				e.Generation += 2
				e.Status.ObservedGeneration = e.Generation
			}}
		}, appReleaseUnidentified, false},
		{"pinned superseded is terminal", "Deploying", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(e *client.AppEnvironment, _ *[]client.AppRelease) {}, func(e *client.AppEnvironment, releases *[]client.AppRelease) {
				(*releases)[0].Status.Phase = "Superseded"
				e.Generation++
				e.Status.RequestedRelease = &client.AppReleaseRef{Name: "new-request"}
			}}
		}, "current requested release is", false},
		{"old observed generation never succeeds", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(e *client.AppEnvironment, _ *[]client.AppRelease) { e.Status.ObservedGeneration = 0 }}
		}, "timeout ended", false},
		{"degraded succeeded workload fails after grace", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			old := appDegradedGrace
			appDegradedGrace = 0
			t.Cleanup(func() { appDegradedGrace = old })
			e.Status.Phase = "Degraded"
		}, "remained Degraded", false},
		{"identity changes after history", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentHistoryHook = func(e *client.AppEnvironment) { e.UID = "replacement" }
		}, "old release does not verify", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			model := appTestEnvironmentModel()
			web, secrets, domains, d := appEnvironmentSettings(context.Background(), &model)
			if d.HasError() {
				t.Fatal(d)
			}
			env := &client.AppEnvironment{App: "shop", Environment: "production", Name: "shop-production", Cluster: "europe", UID: "original", Generation: 1, DeploymentRevision: 1, DeletionProtection: true, Web: web, Secrets: secrets, Domains: domains, Status: &client.AppEnvironmentStatus{Namespace: "app-shop-production"}}
			mock.apps["shop"] = map[string]any{"name": "shop"}
			mock.appEnvironments["shop/production"] = env
			mock.appEnvironmentOutcome = tc.phase
			mock.appMockRelease("shop/production", env)
			accepted := *env
			if tc.mutate != nil {
				tc.mutate(mock, env)
			}
			r := NewAppEnvironmentResource().(*AppEnvironmentResource)
			r.client = client.New(mock.url(), "acme", "test-token")
			sch := testResourceSchema(t, r)
			var state tfsdk.State
			state.Schema = sch.Schema
			mapAppEnvironmentToState(&accepted, mockAppEnvironmentETag(&accepted), &model)
			// Populate timeouts through the framework's schema conversion.
			model.Timeouts = typesTimeoutsNull(t, sch)
			if d := state.Set(context.Background(), &model); d.HasError() {
				t.Fatal(d)
			}
			var diags diag.Diagnostics
			r.waitAppEnvironment(context.Background(), 100*time.Millisecond, tc.op, &accepted, &model, true, &state, &diags)
			if tc.warning && (diags.HasError() || len(diags.Warnings()) == 0) {
				t.Fatalf("want warning only: %v", diags)
			}
			if !tc.warning && !diags.HasError() {
				t.Fatalf("want failure: %v", diags)
			}
			text := ""
			for _, d := range diags {
				text += d.Detail()
			}
			if !strings.Contains(text, tc.want) {
				t.Fatalf("missing %q: %s", tc.want, text)
			}
			if strings.Contains(text, "next `terraform apply` plans no change") {
				t.Fatal("diagnostic incorrectly promises no drift after foreign configuration")
			}
			if model.Web.IsNull() || !model.Web.Equal(appWebToObject(web)) {
				t.Fatal("wait discarded accepted configuration")
			}
		})
	}
}

func typesTimeoutsNull(t *testing.T, sch resource.SchemaResponse) timeouts.Value {
	t.Helper()
	state := tfsdk.State{Schema: sch.Schema, Raw: testPlanValue(t, sch, map[string]tftypes.Value{})}
	var value timeouts.Value
	if d := state.GetAttribute(context.Background(), path.Root("timeouts"), &value); d.HasError() {
		t.Fatal(d)
	}
	return value
}
