package provider

import (
	"fmt"
	"math"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccClusterWaitConfig is a kupe_cluster "test" named "wait" with
// limits cpu and extra lines (e.g. timeouts).
func testAccClusterWaitConfig(host, cpu, extra string) string {
	return testAccProviderConfig(host) + fmt.Sprintf(`
resource "kupe_cluster" "test" {
  name      = "wait"
  resources = { cpu = %q }
%s
}
`, cpu, extra)
}

// testAccCheckNoClusters verifies destroy removed every cluster through the
// API.
func testAccCheckNoClusters(mock *mockKupeAPI) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.clusters) != 0 {
			return fmt.Errorf("expected no clusters, got %d", len(mock.clusters))
		}
		return nil
	}
}

// testAccCheckClusterStatusesPlayed checks the wait read every scripted
// status: only the last, repeating one is left.
func testAccCheckClusterStatusesPlayed(mock *mockKupeAPI) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.clusterStatuses) != 1 {
			return fmt.Errorf("%d scripted statuses left, want only the last: the wait returned early", len(mock.clusterStatuses))
		}
		return nil
	}
}

// setClusterDegradedGrace shortens or lengthens clusterDegradedGrace for one
// test.
func setClusterDegradedGrace(t *testing.T, grace time.Duration) {
	t.Helper()
	prev := clusterDegradedGrace
	clusterDegradedGrace = grace
	t.Cleanup(func() { clusterDegradedGrace = prev })
}

// TestAccClusterResource_UpdateWait: Update waits until the cluster is
// Running at the post-PATCH generation, rides out Degraded at that
// generation for clusterDegradedGrace, fails on Degraded beyond the grace
// and at once on a cluster being deleted.
func TestAccClusterResource_UpdateWait(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_cluster.test"
	// The PATCH made generation 2; observedGeneration 1 is the spec before.
	stale := map[string]any{"phase": "Running", "observedGeneration": 1}
	converged := map[string]any{}
	syncFailed := []any{map[string]any{"type": "Ready", "status": "False", "message": "cluster deployment sync failed"}}
	degradedBefore := map[string]any{"phase": "Degraded", "observedGeneration": 1, "conditions": syncFailed}
	degradedSeen := map[string]any{"phase": "Degraded", "observedGeneration": 2, "conditions": syncFailed}
	tests := []struct {
		name          string
		grace         time.Duration
		updateTimeout string
		statuses      []map[string]any
		wantErr       string // empty: the update applies
	}{
		{
			name: "waits for the post-PATCH generation and through Upgrading", grace: time.Hour,
			statuses: []map[string]any{stale, {"phase": "Upgrading", "observedGeneration": 2}, converged},
		},
		{
			// The order the platform produces for a cluster Degraded by a
			// failed deployment sync: the first poll reads the status from
			// before the PATCH; the pass that takes the change stamps the new
			// generation but judges the phase from the deployment's status
			// from before its retry, so it still reads Degraded; the retry
			// heals it.
			name: "a Degraded cluster the change heals is not a failure", grace: clusterDegradedGrace,
			statuses: []map[string]any{degradedBefore, degradedSeen, degradedSeen, converged},
		},
		{
			name: "fails once Degraded at the new generation outlasts the grace", grace: 0,
			statuses: []map[string]any{degradedBefore, degradedSeen},
			wantErr: `(?s)cluster degraded.*` + wrappedText(`Cluster "wait" is Degraded, so the update did not complete.`) +
				`.*` + wrappedText("Status message: cluster deployment sync failed"),
		},
		{
			name: "a timeout while still Degraded is an error", grace: time.Hour, updateTimeout: "1s",
			statuses: []map[string]any{degradedSeen},
			wantErr:  `(?s)cluster degraded.*` + wrappedText("The update timeout fired while it was Degraded."),
		},
		{
			name: "fails at once on a cluster being deleted", grace: time.Hour,
			statuses: []map[string]any{{"phase": "Terminating"}},
			wantErr:  `(?s)cluster terminating.*` + wrappedText("It is being deleted, so it can no longer change."),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setClusterDegradedGrace(t, tt.grace)
			mock := newMockKupeAPI()
			defer mock.close()
			updateTimeout := tt.updateTimeout
			if updateTimeout == "" {
				updateTimeout = "1m"
			}
			step := resource.TestStep{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.clusterStatusesOnPatch = tt.statuses })
				},
				Config: testAccClusterWaitConfig(mock.url(), "4", fmt.Sprintf("  timeouts = { update = %q }", updateTimeout)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "resources.cpu", "4"),
					resource.TestCheckResourceAttr(addr, "phase", "Running"),
					testAccCheckClusterStatusesPlayed(mock),
				),
			}
			if tt.wantErr != "" {
				step.Check, step.ExpectError = nil, regexp.MustCompile(tt.wantErr)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoClusters(mock),
				Steps:                    []resource.TestStep{{Config: testAccClusterWaitConfig(mock.url(), "2", "")}, step},
			})
		})
	}
}

// TestAccClusterResource_DegradedCreate: a cluster that turns Degraded while
// it is created fails the apply and stays in state marked tainted, so the
// next apply replaces it.
func TestAccClusterResource_DegradedCreate(t *testing.T) {
	shrinkPollInterval(t)
	setClusterDegradedGrace(t, 0)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) { m.clusterStatuses = []map[string]any{{"phase": "Degraded"}} })
	config := testAccClusterWaitConfig(mock.url(), "2", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusters(mock),
		Steps: []resource.TestStep{
			{
				Config: config,
				ExpectError: regexp.MustCompile(`(?s)cluster degraded.*` +
					wrappedText("marked tainted, so the next `terraform apply` plans to replace it.")),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.clusterStatuses = nil }) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("kupe_cluster.test", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("kupe_cluster.test", "phase", "Running"),
			},
		},
	})
}

// TestAccClusterResource_UpdateAfterConcurrentWrite: a cluster update sent
// with a stale ETag is retried once when only the status moved, and refused
// with the changed setting named when another client changed the spec.
func TestAccClusterResource_UpdateAfterConcurrentWrite(t *testing.T) {
	shrinkPollInterval(t)
	statusWrite := func(c map[string]any) { c["status"] = map[string]any{"phase": "Running", "observedGeneration": 1} }
	specWrite := func(c map[string]any) {
		c["version"] = "1.34"
		c["generation"] = c["generation"].(int) + 1
	}
	tests := []struct {
		name        string
		beforePatch []func(map[string]any)
		wantErr     string
	}{
		{name: "a status write is retried with the fresh ETag", beforePatch: []func(map[string]any){statusWrite}},
		{
			name:        "the retry happens once",
			beforePatch: []func(map[string]any){statusWrite, statusWrite},
			wantErr:     `(?s)kupe API returned 412.*` + wrappedText("The cluster kept changing while the update was being sent"),
		},
		{
			name:        "another client's spec change is reported",
			beforePatch: []func(map[string]any){specWrite},
			wantErr:     `(?s)cluster changed outside this apply.*\(version now differs`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			step := resource.TestStep{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.clusterBeforePatch = tt.beforePatch })
				},
				Config: testAccClusterWaitConfig(mock.url(), "4", ""),
				Check:  resource.TestCheckResourceAttr("kupe_cluster.test", "resources.cpu", "4"),
			}
			if tt.wantErr != "" {
				step.Check, step.ExpectError = nil, regexp.MustCompile(tt.wantErr)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoClusters(mock),
				Steps:                    []resource.TestStep{{Config: testAccClusterWaitConfig(mock.url(), "2", "")}, step},
			})
		})
	}
}

// TestAccClusterResource_DeleteConflicts: kupe-api pins a cluster DELETE to
// what it read and retries it; a conflict that outlasts its retries reaches
// the provider as a 409, which is retried until the delete timeout and then
// reported with a hint about the delete.
func TestAccClusterResource_DeleteConflicts(t *testing.T) {
	shrinkPollInterval(t)

	t.Run("a passing conflict is retried", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoClusters(mock),
			Steps: []resource.TestStep{
				{Config: testAccClusterWaitConfig(mock.url(), "2", "")},
				{
					PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.clusterDeleteConflicts = 2 }) },
					Config:    testAccProviderConfig(mock.url()),
				},
			},
		})
	})

	t.Run("a persistent conflict times out with the delete hint", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoClusters(mock),
			Steps: []resource.TestStep{
				{Config: testAccClusterWaitConfig(mock.url(), "2", `  timeouts = { delete = "1s" }`)},
				{
					PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.clusterDeleteConflicts = math.MaxInt }) },
					Config:    testAccProviderConfig(mock.url()),
					ExpectError: regexp.MustCompile(`(?s)failed to delete cluster.*delete not accepted before the timeout: ` +
						`kupe API returned 409.*kept refusing the delete`),
				},
				{
					PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.clusterDeleteConflicts = 0 }) },
					Config:    testAccProviderConfig(mock.url()),
				},
			},
		})
	})
}
