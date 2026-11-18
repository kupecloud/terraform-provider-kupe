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
	// Cases that reach a terminal outcome get a timeout far above any load
	// delay, so they end on the outcome and never on the clock. Only the case
	// that asserts the timeout itself uses a short one; it can only end by
	// expiry, so load cannot change its result.
	const settleTimeout = time.Minute
	const expireTimeout = 100 * time.Millisecond
	for _, tc := range []struct {
		name, phase, op string
		mutate          func(*mockKupeAPI, *client.AppEnvironment)
		want            string
		warning         bool
		expires         bool
	}{
		{"failed create warns", "Failed", "create", nil, "Requested release", true, false},
		{"failed update errors", "Failed", "update", nil, "Requested release", false, false},
		{"recovered request remains failed", "Failed", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			releases := m.appReleases["shop/production"]
			releases[0].Status.Recovery = &client.AppReleaseRecovery{Phase: "Succeeded", Release: &client.AppReleaseRef{Name: "previous"}}
			m.appReleases["shop/production"] = releases
			e.Status.ActiveRelease = &client.AppReleaseRef{Name: "previous"}
		}, "now serving", false, false},
		{"prepin generation is unverified", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(e *client.AppEnvironment, _ *[]client.AppRelease) {
				e.Generation += 2
				e.Status.ObservedGeneration = e.Generation
			}}
		}, appReleaseUnidentified, false, false},
		{"pinned superseded is terminal", "Deploying", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(e *client.AppEnvironment, _ *[]client.AppRelease) {}, func(e *client.AppEnvironment, releases *[]client.AppRelease) {
				(*releases)[0].Status.Phase = "Superseded"
				e.Generation++
				e.Status.RequestedRelease = &client.AppReleaseRef{Name: "new-request"}
			}}
		}, "current requested release is", false, false},
		{"old observed generation never succeeds", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(e *client.AppEnvironment, _ *[]client.AppRelease) { e.Status.ObservedGeneration = 0 }}
		}, "timeout ended", false, true},
		{"degraded succeeded workload fails after grace", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			old := appDegradedGrace
			appDegradedGrace = 0
			t.Cleanup(func() { appDegradedGrace = old })
			e.Status.Phase = "Degraded"
		}, "remained Degraded", false, false},
		{"identity changes after history", "Succeeded", "update", func(m *mockKupeAPI, e *client.AppEnvironment) {
			m.appEnvironmentHistoryHook = func(e *client.AppEnvironment) { e.UID = "replacement" }
		}, "old release does not verify", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			model := appTestEnvironmentModel()
			runtime, secrets, databases, domains, d := appEnvironmentSettings(context.Background(), &model)
			if d.HasError() {
				t.Fatal(d)
			}
			env := &client.AppEnvironment{App: "shop", Environment: "production", Name: "shop-production", Cluster: "europe", UID: "original", Generation: 1, DeploymentRevision: 1, DeletionProtection: true, Runtime: runtime, Secrets: secrets, Databases: databases, Domains: domains, Status: &client.AppEnvironmentStatus{Namespace: "app-shop-production"}}
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
			timeout := settleTimeout
			if tc.expires {
				timeout = expireTimeout
			}
			r.waitAppEnvironment(context.Background(), timeout, tc.op, &accepted, &model, true, &state, &diags)
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
			if model.Runtime.IsNull() || !model.Runtime.Equal(appRuntimeToObject(runtime)) {
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

// Another writer replaces the connections after the PATCH was accepted. The
// wait reports it and keeps the accepted connections in state, never the
// foreign ones, so the next plan shows the drift.
func TestAppEnvironmentWaitKeepsAcceptedDatabasesAfterForeignChange(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	model := appTestEnvironmentModel()
	model.Databases = appTestDatabases(appTestDatabase("main", "orders-db", "", "DATABASE_URL"))
	runtime, secrets, databases, domains, d := appEnvironmentSettings(context.Background(), &model)
	if d.HasError() {
		t.Fatal(d)
	}
	env := &client.AppEnvironment{App: "shop", Environment: "production", Name: "shop-production", Cluster: "europe", UID: "original", Generation: 1, DeploymentRevision: 1, DeletionProtection: true, Runtime: runtime, Secrets: secrets, Databases: databases, Domains: domains, Status: &client.AppEnvironmentStatus{Namespace: "app-shop-production"}}
	mock.apps["shop"] = map[string]any{"name": "shop"}
	mock.appEnvironments["shop/production"] = env
	mock.appEnvironmentOutcome = "Deploying"
	mock.appMockRelease("shop/production", env)
	accepted := *env
	foreign := []client.AppDatabase{{Name: "main", Database: "orders-db", Env: "FOREIGN_URL"}, {Name: "extra", Database: "users-db", Env: "USERS_URL"}}
	mock.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(e *client.AppEnvironment, _ *[]client.AppRelease) {
		e.Databases = foreign
		e.Generation++
		e.Status.ObservedGeneration = e.Generation
	}}
	r := NewAppEnvironmentResource().(*AppEnvironmentResource)
	r.client = client.New(mock.url(), "acme", "test-token")
	sch := testResourceSchema(t, r)
	state := tfsdk.State{Schema: sch.Schema}
	mapAppEnvironmentToState(&accepted, mockAppEnvironmentETag(&accepted), &model)
	model.Timeouts = typesTimeoutsNull(t, sch)
	if d := state.Set(context.Background(), &model); d.HasError() {
		t.Fatal(d)
	}
	var diags diag.Diagnostics
	r.waitAppEnvironment(context.Background(), time.Minute, "update", &accepted, &model, true, &state, &diags)
	if !diags.HasError() || !strings.Contains(diags.Errors()[0].Detail(), appReleaseUnidentified) {
		t.Fatalf("foreign change was not reported: %v", diags)
	}
	want := appDatabasesToList(databases)
	var recorded AppEnvironmentResourceModel
	if d := state.Get(context.Background(), &recorded); d.HasError() {
		t.Fatal(d)
	}
	if !model.Databases.Equal(want) || !recorded.Databases.Equal(want) {
		t.Fatalf("state databases = %v, want the accepted %v", recorded.Databases, want)
	}
}
