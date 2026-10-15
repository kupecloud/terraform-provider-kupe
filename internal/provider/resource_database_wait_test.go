package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// setDegradedGrace shortens or lengthens databaseDegradedGrace for one test.
func setDegradedGrace(t *testing.T, grace time.Duration) {
	t.Helper()
	prev := databaseDegradedGrace
	databaseDegradedGrace = grace
	t.Cleanup(func() { databaseDegradedGrace = prev })
}

// testAccCheckDatabaseStatusesPlayed checks the wait read every scripted
// status: only the last, repeating one is left.
func testAccCheckDatabaseStatusesPlayed(mock *mockKupeAPI) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.databaseStatuses) != 1 {
			return fmt.Errorf("%d scripted statuses left, want only the last: the wait returned early", len(mock.databaseStatuses))
		}
		return nil
	}
}

// TestAccDatabaseResource_UpdateWait: Update waits until the change is in
// effect on every instance (Running, observedGeneration caught up and Ready
// True), rides out Degraded for databaseDegradedGrace, fails on Terminating
// and on Degraded beyond the grace, and only warns when the timeout fires on
// a change still rolling out.
func TestAccDatabaseResource_UpdateWait(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database.test"
	rolling := mockDatabaseStatus("Running", false, "applying changes: Cluster in healthy state (1 instances ready)")
	degraded := mockDatabaseStatus("Degraded", false, "Waiting for the instances to become active (0 instances ready)")
	terminating := mockDatabaseStatus("Terminating", false, "the database is being deleted")
	ready := mockDatabaseStatus("Running", true, "1 of 1 instances ready")
	tests := []struct {
		name          string
		grace         time.Duration
		updateTimeout string
		statuses      []map[string]any
		wantErr       string // empty: the update applies
		wantPhase     string
	}{
		{
			name: "waits while the instances roll onto the change", grace: time.Hour,
			statuses: []map[string]any{rolling, rolling, ready}, wantPhase: "Running",
		},
		{
			name: "rides out Degraded within the grace", grace: time.Hour,
			statuses: []map[string]any{degraded, degraded, ready}, wantPhase: "Running",
		},
		{
			// observedGeneration 1 is the spec before the PATCH (generation 2).
			name: "Degraded from before the change is not a failure", grace: 0,
			statuses: []map[string]any{
				{"phase": "Degraded", "observedGeneration": 1}, {"phase": "Degraded", "observedGeneration": 1}, ready,
			},
			wantPhase: "Running",
		},
		{
			name: "fails once Degraded outlasts the grace", grace: 0,
			statuses: []map[string]any{degraded},
			wantErr: `(?s)database degraded.*` + wrappedText(`Database "orders-db" is Degraded, so the update did not complete.`) +
				`.*` + wrappedText("Status message: Waiting for the instances to become active (0 instances ready)"),
		},
		{
			name: "fails at once on a database being deleted", grace: time.Hour,
			statuses: []map[string]any{terminating},
			wantErr: `(?s)database terminating.*` + wrappedText(`Database "orders-db" is Terminating`) +
				`.*` + wrappedText("It is being deleted, so it can no longer change."),
		},
		{
			name: "a timeout while still Degraded is an error", grace: time.Hour, updateTimeout: "1s",
			statuses: []map[string]any{degraded},
			wantErr:  `(?s)database degraded.*` + wrappedText("The update timeout fired while it was Degraded."),
		},
		{
			name: "a timeout while still rolling out only warns", updateTimeout: "1s",
			statuses: []map[string]any{rolling}, wantPhase: "Running",
		},
		{
			// The operator failed to apply generation 2 and retries: it
			// leaves observedGeneration and the phase as they were.
			name: "a timeout while the platform fails to apply the change is an error", grace: time.Hour,
			updateTimeout: "1s",
			statuses: []map[string]any{{"phase": "Running", "observedGeneration": 1, "conditions": []any{map[string]any{
				"type": "Ready", "status": "False", "reason": "ReconcileFailed",
				"message": "the database could not be reconciled; retrying",
			}}}},
			wantErr: `(?s)database update not applied.*` +
				wrappedText(`The Kupe platform has not applied the change to database "orders-db"`) +
				`.*` + wrappedText("Status message: the database could not be reconciled; retrying"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDegradedGrace(t, tt.grace)
			mock := newMockKupeAPI()
			defer mock.close()
			base := testDatabase{protection: boolPtr(false), updateTimeout: tt.updateTimeout}
			resized := base
			resized.cpu = "1"
			step := resource.TestStep{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.databaseStatusesOnPatch = tt.statuses })
				},
				Config: testAccDatabaseConfig(mock.url(), resized),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "resources.cpu", "1"),
					resource.TestCheckResourceAttr(addr, "phase", tt.wantPhase),
					testAccCheckDatabaseStatusesPlayed(mock),
				),
			}
			if tt.wantErr != "" {
				step.Check, step.ExpectError = nil, regexp.MustCompile(tt.wantErr)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps:                    []resource.TestStep{{Config: testAccDatabaseConfig(mock.url(), base)}, step},
			})
		})
	}
}

// TestAccDatabaseResource_ProtectionChangeNotWaited: a change to
// deletion_protection alone is in effect once the PATCH is accepted, so
// Update does not wait, even on a database that would fail the wait.
func TestAccDatabaseResource_ProtectionChangeNotWaited(t *testing.T) {
	shrinkPollInterval(t)
	setDegradedGrace(t, 0)
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(true)})},
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.databaseStatusesOnPatch = []map[string]any{
							mockDatabaseStatus("Degraded", false, "Waiting for the instances to become active"),
						}
					})
				},
				Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}),
				Check:  resource.TestCheckResourceAttr("kupe_database.test", "deletion_protection", "false"),
			},
			{
				// The same database fails a change that is waited for.
				Config:      testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false), cpu: "1"}),
				ExpectError: regexp.MustCompile(`database degraded`),
			},
		},
	})
}

// TestAccDatabaseResource_CreateWait: Create fails on a database that ends
// Degraded, keeping it in state marked tainted, and only warns when the
// timeout fires while it is still provisioning.
func TestAccDatabaseResource_CreateWait(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database.test"

	t.Run("Degraded fails the create and taints the database", func(t *testing.T) {
		setDegradedGrace(t, 0)
		mock := newMockKupeAPI()
		defer mock.close()
		mock.setState(func(m *mockKupeAPI) {
			m.databaseStatuses = []map[string]any{mockDatabaseStatus("Degraded", false, "")}
		})
		config := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{
					Config: config,
					ExpectError: regexp.MustCompile(`(?s)database degraded.*` +
						wrappedText(`Database "orders-db" is Degraded, so the create did not complete.`) +
						`.*` + wrappedText("marked tainted, so the next `terraform apply` plans to replace it.")),
				},
				{
					PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.databaseStatuses = nil }) },
					Config:    config,
					// The tainted database is replaced.
					ConfigPlanChecks: expectAction(plancheck.ResourceActionDestroyBeforeCreate),
					Check:            resource.TestCheckResourceAttr(addr, "phase", "Running"),
				},
			},
		})
	})

	t.Run("a timeout while provisioning only warns", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		mock.setState(func(m *mockKupeAPI) {
			m.databaseStatuses = []map[string]any{mockDatabaseStatus("Provisioning", false, "creating the database")}
		})
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false), createTimeout: "1s"}),
				Check:  resource.TestCheckResourceAttr(addr, "phase", "Provisioning"),
			}},
		})
	})
}

// TestAccDatabaseResource_CodedRefusals: a refusal kupe-api names with a
// code gets that code's remedy, not the generic "name already exists" hint.
func TestAccDatabaseResource_CodedRefusals(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.databaseRefusals = map[string]mockRefusal{"POST": {http.StatusConflict, client.CodePaymentSetupRequired,
							"tenant acme is awaiting payment setup; add a payment method before creating databases"}}
					})
				},
				Config: config,
				ExpectError: regexp.MustCompile(`(?s)failed to create database.*Error code: payment_setup_required.*` +
					wrappedText("Add a payment method on the Billing page of the Kupe Console")),
			},
			{Config: config},
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.databaseRefusals = map[string]mockRefusal{"PATCH": {http.StatusConflict, client.CodeTerminating,
							`database "orders-db" is being deleted`}}
					})
				},
				Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false), cpu: "1"}),
				ExpectError: regexp.MustCompile(`(?s)failed to update database.*Error code: terminating.*` +
					wrappedText("It is being deleted, so it can no longer be changed or bound.")),
			},
		},
	})
}

// TestAccHighAvailabilityKeptWhenUnset: high_availability has no default,
// so a configuration without it keeps what the resource has. Removing it
// from configuration stands in for importing an HA resource into a
// configuration without it: both plan with the attribute null and true in
// state. A default used to plan HA off for a database and a replacement for
// a cluster.
func TestAccHighAvailabilityKeptWhenUnset(t *testing.T) {
	shrinkPollInterval(t)
	emptyPlan := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}

	t.Run("database", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		const addr = "kupe_database.test"
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{
					Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false), highAvailability: boolPtr(true)}),
					Check:  resource.TestCheckResourceAttr(addr, "high_availability", "true"),
				},
				{
					Config:           testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}),
					ConfigPlanChecks: emptyPlan,
					Check:            resource.TestCheckResourceAttr(addr, "high_availability", "true"),
				},
			},
		})
	})

	t.Run("cluster", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		const addr = "kupe_cluster.test"
		withHA := testAccClusterConfigNoDisplayName(mock.url(), "ha-test")
		withHA = withHA[:len(withHA)-len("}\n")] + "  high_availability = true\n}\n"
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			Steps: []resource.TestStep{
				{
					Config: withHA,
					Check:  resource.TestCheckResourceAttr(addr, "high_availability", "true"),
				},
				{
					Config:           testAccClusterConfigNoDisplayName(mock.url(), "ha-test"),
					ConfigPlanChecks: emptyPlan,
					Check:            resource.TestCheckResourceAttr(addr, "high_availability", "true"),
				},
			},
		})
	})

	t.Run("a new database or cluster without it is created without HA", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			Steps: []resource.TestStep{{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}) +
					`resource "kupe_cluster" "test" { name = "no-ha" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_database.test", "high_availability", "false"),
					resource.TestCheckResourceAttr("kupe_cluster.test", "high_availability", "false"),
				),
			}},
		})
	})
}
