package provider

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// upgradeDB is the database the major upgrade tests configure at version v.
func upgradeDB(version string, updateTimeout string) testDatabase {
	return testDatabase{version: version, protection: boolPtr(false), updateTimeout: updateTimeout}
}

// testAccCheckDatabaseVersionRequests checks the version of each database
// PATCH body, in order ("" for a PATCH without one).
func testAccCheckDatabaseVersionRequests(mock *mockKupeAPI, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		var got []string
		for _, req := range mock.databaseRequests {
			if req.method != "PATCH" {
				continue
			}
			v, _ := req.body["version"].(string)
			got = append(got, v)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("PATCH versions = %q, want %q", got, want)
		}
		return nil
	}
}

// TestAccDatabaseResource_MajorUpgrade: raising version is an update in
// place (never a replacement) that sends the new major, waits through the
// pre-upgrade backup and Upgrading until the database runs it, and records
// the new version.
func TestAccDatabaseResource_MajorUpgrade(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database.test"
	mock := newMockKupeAPI()
	defer mock.close()
	mock.majorUpgrades = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", "")),
				Check:  resource.TestCheckResourceAttr(addr, "current_version", "17.0"),
			},
			{
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", "")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue(addr, tfjsonpath.New("version"), knownvalue.StringExact("18")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "version", "18"),
					resource.TestCheckResourceAttr(addr, "current_version", "18.1"),
					resource.TestCheckResourceAttr(addr, "phase", "Running"),
					testAccCheckDatabaseStatusesPlayed(mock),
					testAccCheckDatabaseVersionRequests(mock, "18"),
				),
			},
			{
				// Nothing is left to do once the database runs 18.
				Config:   testAccDatabaseConfig(mock.url(), upgradeDB("18", "")),
				PlanOnly: true,
			},
		},
	})
}

// TestAccDatabaseResource_MajorUpgradeOmittedVersionKeepsIt: an edit that
// leaves version unset after an upgrade neither sends a version nor lowers it.
func TestAccDatabaseResource_MajorUpgradeOmittedVersionKeepsIt(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.majorUpgrades = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", ""))},
			{Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", ""))},
			{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{cpu: "1", protection: boolPtr(false)}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_database.test", "version", "18"),
					testAccCheckDatabaseVersionRequests(mock, "18", ""),
				),
			},
		},
	})
}

// TestAccDatabaseResource_MajorUpgradeUnavailable: with the platform switch
// off a raise plans as an update, the apply fails with the API's code and the
// remedy, and nothing is recorded: the next plan is empty.
func TestAccDatabaseResource_MajorUpgradeUnavailable(t *testing.T) {
	const addr = "kupe_database.test"
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", ""))},
			{
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", "")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				ExpectError: regexp.MustCompile(`(?s)` + client.CodeMajorUpgradeUnavailable + `.*` +
					wrappedText("Major version upgrades are not offered on this platform right now, so nothing changed.")),
			},
			{
				Config:   testAccDatabaseConfig(mock.url(), upgradeDB("17", "")),
				PlanOnly: true,
			},
		},
	})
}

// TestAccDatabaseResource_MajorUpgradeLoweringIsRefused: a lowering that is
// not a cancel fails the plan, before anything is sent.
func TestAccDatabaseResource_MajorUpgradeLoweringIsRefused(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", ""))},
			{
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", "")),
				ExpectError: regexp.MustCompile(`(?s)Database major version can only move up.*` +
					wrappedText(`cannot change to "17": a database's major version can only move up.`) + `.*` +
					wrappedText("Set it back to the major the database runs (18) only to cancel an upgrade.")),
			},
			{
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", "")),
				Check:  testAccCheckDatabaseVersionRequests(mock),
			},
		},
	})
}

// TestAccDatabaseResource_MajorUpgradeEnds: an upgrade the platform rolls
// back or fails ends the apply at once (even Degraded, whatever the grace),
// with the operator's message and how to cancel; the follow-up apply with
// version set back to the running major cancels it and converges, with the
// platform switch on and off (NEW-B-1).
func TestAccDatabaseResource_MajorUpgradeEnds(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database.test"
	tests := []struct {
		name     string
		outcome  string
		switchOn bool
		wantErr  string
		wantRun  string
	}{
		{
			name: "rolled back, switch on", outcome: mockUpgradeRolledBack, switchOn: true,
			wantErr: `(?s)database upgrade did not complete.*` +
				wrappedText(`The upgrade of database "orders-db" to major version 18 rolled back. It reports version 17.0.`) +
				`.*` + wrappedText("Status message: the upgrade stopped before it changed any data and the database runs its old major"),
		},
		{
			name: "rolled back, switch off", outcome: mockUpgradeRolledBack, switchOn: false,
			wantErr: `(?s)database upgrade did not complete.*` + wrappedText("rolled back"),
		},
		{
			name: "failed", outcome: mockUpgradeFailed, switchOn: true,
			wantErr: `(?s)database upgrade did not complete.*` + wrappedText("to major version 18 failed.") +
				`.*` + wrappedText("Status message: the upgrade failed: the database runs its old major"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A grace this long would hold a Degraded database for the whole
			// test: the failure must not wait for it.
			setDegradedGrace(t, time.Hour)
			mock := newMockKupeAPI()
			defer mock.close()
			mock.majorUpgrades, mock.upgradeOutcome = true, tt.outcome

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps: []resource.TestStep{
					{Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", ""))},
					{
						Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", "")),
						ExpectError: regexp.MustCompile(tt.wantErr + `.*` + wrappedText("`version = \"17\"`") +
							`.*` + wrappedText("`terraform plan` shows no change")),
					},
					{
						// The change is in state and the database runs 17: the
						// cancel is an in-place update, never a replacement.
						PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.majorUpgrades = tt.switchOn }) },
						Config:    testAccDatabaseConfig(mock.url(), upgradeDB("17", "")),
						ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
							plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						}},
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr(addr, "version", "17"),
							resource.TestCheckResourceAttr(addr, "current_version", "17.0"),
							resource.TestCheckResourceAttr(addr, "phase", "Running"),
							testAccCheckDatabaseVersionRequests(mock, "18", "17"),
						),
					},
				},
			})
		})
	}
}

// TestAccDatabaseResource_MajorUpgradeCancelDuringBackup: while the platform
// takes the pre-upgrade backup (phase Running, Ready False), version set back
// to the running major plans as an in-place cancel, with the switch off too.
func TestAccDatabaseResource_MajorUpgradeCancelDuringBackup(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database.test"
	mock := newMockKupeAPI()
	defer mock.close()
	mock.majorUpgrades = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", ""))},
			{
				// The wait ends on its timeout while the backup is taken.
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.upgradeOutcome = mockUpgradeBackup })
				},
				Config:      testAccDatabaseConfig(mock.url(), upgradeDB("18", "1s")),
				ExpectError: regexp.MustCompile(`(?s)database update timed out.*` + wrappedText("The upgrade is at PreUpgradeBackup.")),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.majorUpgrades = false }) },
				Config:    testAccDatabaseConfig(mock.url(), upgradeDB("17", "")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "version", "17"),
			},
		},
	})
}

// TestAccDatabaseResource_MajorUpgradeTimeoutHints: a timeout on an upgrade
// that waits or takes its backup says where it is and how to cancel.
func TestAccDatabaseResource_MajorUpgradeTimeoutHints(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.majorUpgrades, mock.upgradeOutcome = true, mockUpgradeWaiting

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", ""))},
			{
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", "1s")),
				ExpectError: regexp.MustCompile(`(?s)Status message: waiting for a backup from the last 26 hours.*` +
					wrappedText("The upgrade is at UpgradeWaiting. It starts once the check in the status message is met. "+
						"To cancel, set `version = \"17\"` and run `terraform apply`.") + `.*` + wrappedText(changeRecordedNotice)),
			},
			{
				// Cancel it so the database can be destroyed.
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", "")),
				Check:  resource.TestCheckResourceAttr("kupe_database.test", "version", "17"),
			},
		},
	})
}

// TestAccDatabaseResource_MajorUpgradeRefusals: the operator's refusals reach
// the user with its message and code, and nothing is recorded.
func TestAccDatabaseResource_MajorUpgradeRefusals(t *testing.T) {
	tests := []struct {
		name    string
		version string
		refuse  *mockRefusal
		wantErr string
	}{
		{
			name: "two majors at once is not ready", version: "19",
			wantErr: `(?s)` + wrappedText("kupe API returned 409: the database runs 17.0: upgrades go one major at a time") + `.*` +
				`Error code: DATABASE_UPGRADE_NOT_READY\s+Field: spec.version.*` +
				wrappedText("The database cannot be upgraded yet, so nothing changed."),
		},
		{
			name: "the operator's message is kept", version: "18",
			refuse: &mockRefusal{status: 409, code: client.CodeDatabaseUpgradeNotReady,
				message: "the database has no backup from the last 26 hours"},
			wantErr: `(?s)the database has no backup from the last 26 hours.*DATABASE_UPGRADE_NOT_READY`,
		},
		{
			name: "an upgrade in progress", version: "18",
			refuse: &mockRefusal{status: 409, code: client.CodeDatabaseUpgradeInProgress,
				message: "an upgrade of orders-db is in progress"},
			wantErr: `(?s)` + wrappedText("kupe API returned 409: an upgrade of orders-db is in progress") + `.*` +
				`Error code: DATABASE_UPGRADE_IN_PROGRESS.*` + wrappedText("The database is being upgraded, so nothing changed."),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			mock.majorUpgrades = true

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps: []resource.TestStep{
					{Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", ""))},
					{
						PreConfig: func() {
							if tt.refuse != nil {
								mock.setState(func(m *mockKupeAPI) { m.databaseRefusals = map[string]mockRefusal{"PATCH": *tt.refuse} })
							}
						},
						Config:      testAccDatabaseConfig(mock.url(), upgradeDB(tt.version, "")),
						ExpectError: regexp.MustCompile(tt.wantErr),
					},
					{
						Config:   testAccDatabaseConfig(mock.url(), upgradeDB("17", "")),
						PlanOnly: true,
					},
				},
			})
		})
	}
}

// TestAccDatabaseResource_MajorUpgradeCancelledElsewhere: another client
// cancels the upgrade while this apply waits for it. The apply must fail, not
// report success on the old major, and record what the database holds.
func TestAccDatabaseResource_MajorUpgradeCancelledElsewhere(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.majorUpgrades, mock.upgradeOutcome = true, mockUpgradeWaiting

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), upgradeDB("17", ""))},
			{
				PreConfig: func() {
					// Cancel from "another client" once this apply's PATCH is in.
					go func() {
						for {
							done := false
							mock.setState(func(m *mockKupeAPI) {
								patched := false
								for _, req := range m.databaseRequests {
									patched = patched || req.method == "PATCH"
								}
								if !patched {
									return
								}
								m.databases["orders-db"]["version"] = "17"
								m.databaseStatuses = nil
								done = true
							})
							if done {
								return
							}
							time.Sleep(5 * time.Millisecond)
						}
					}()
				},
				Config: testAccDatabaseConfig(mock.url(), upgradeDB("18", "1m")),
				ExpectError: regexp.MustCompile(`(?s)database changed outside this apply.*` +
					wrappedText(`its version is now "17", not the "18" this apply set`)),
			},
			{
				// State holds what the database does: a raise is planned again.
				Config:   testAccDatabaseConfig(mock.url(), upgradeDB("17", "1m")),
				PlanOnly: true,
			},
		},
	})
}

func TestMajorOf(t *testing.T) {
	tests := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"18", 18, true}, {"17.6", 17, true}, {" 9 ", 9, true}, {"18.0.1", 18, true},
		{"", 0, false}, {"latest", 0, false}, {".5", 0, false}, {"-1", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := majorOf(tt.in)
			if ok != tt.wantOK || (ok && got != tt.want) {
				t.Errorf("majorOf(%q) = %d, %v; want %d, %v", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// upgradeDatabase is a database whose spec asks for version, running current
// with the Ready condition and, when set, the UpgradeInProgress condition.
func upgradeDatabase(version, current, phase, readyStatus, readyReason, upgradeStatus, upgradeReason string, observed int64) client.Database {
	conditions := []client.Condition{{Type: "Ready", Status: readyStatus, Reason: readyReason, Message: "the message"}}
	if upgradeStatus != "" {
		conditions = append(conditions, client.Condition{Type: "UpgradeInProgress", Status: upgradeStatus, Reason: upgradeReason})
	}
	return client.Database{
		Name: "orders-db", Version: version, Generation: 3,
		Status: &client.DatabaseStatus{
			Phase: phase, CurrentVersion: current, ObservedGeneration: observed, Conditions: conditions,
		},
	}
}

func TestDatabaseConverged_MajorUpgrade(t *testing.T) {
	tests := []struct {
		name string
		db   client.Database
		want bool
	}{
		{"ready on the new major", upgradeDatabase("18", "18.1", "Running", "True", "Healthy", "False", "UpgradeSucceeded", 3), true},
		{"ready on the old major is not an upgrade", upgradeDatabase("18", "17.6", "Running", "True", "Healthy", "", "", 3), false},
		{"ready on the old major while it waits", upgradeDatabase("18", "17.6", "Running", "True", "UpgradeWaiting", "True", "UpgradeWaiting", 3), false},
		{"offline while upgrading", upgradeDatabase("18", "17.6", "Upgrading", "False", "Upgrading", "True", "Upgrading", 3), false},
		{"new major, standby still rebuilding", upgradeDatabase("18", "18.1", "Running", "False", "StandbyRebuilding", "False", "UpgradeSucceeded", 3), false},
		{"minor of the same major", upgradeDatabase("17", "17.6", "Running", "True", "Healthy", "", "", 3), true},
		{"no running version reported yet", upgradeDatabase("17", "", "Running", "True", "Healthy", "", "", 3), true},
		{"cancelled, back on the running major", upgradeDatabase("17", "17.6", "Running", "True", "Healthy", "False", "UpgradeCancelled", 3), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseConverged(&tt.db); got != tt.want {
				t.Errorf("databaseConverged() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDatabaseKindFailed: the upgrade verdicts, and the ones that are not.
func TestDatabaseKindFailed(t *testing.T) {
	tests := []struct {
		name       string
		db         client.Database
		wantFailed bool
		wantGrace  time.Duration
	}{
		{"rolled back", upgradeDatabase("18", "17.6", "Running", "False", "UpgradeRolledBack", "False", "UpgradeRolledBack", 3), true, 0},
		{"failed while Degraded fails at once", upgradeDatabase("18", "17.6", "Degraded", "False", "UpgradeFailed", "False", "UpgradeFailed", 3), true, 0},
		{
			// A reason left over from an earlier attempt, before the platform has
			// seen the new generation, is not a verdict on this one.
			"rolled back from an earlier attempt", upgradeDatabase("18", "17.6", "Running", "False", "UpgradeRolledBack", "False", "UpgradeRolledBack", 2),
			false, 0,
		},
		{
			// Version already set back: the hold is being cleared.
			"rolled back with the version set back", upgradeDatabase("17", "17.6", "Running", "False", "UpgradeRolledBack", "False", "UpgradeRolledBack", 3),
			false, 0,
		},
		{"waiting is not a failure", upgradeDatabase("18", "17.6", "Running", "False", "UpgradeWaiting", "True", "UpgradeWaiting", 3), false, 0},
		{"backup is not a failure", upgradeDatabase("18", "17.6", "Running", "False", "PreUpgradeBackup", "True", "PreUpgradeBackup", 3), false, 0},
		{"upgrading is not a failure", upgradeDatabase("18", "17.6", "Upgrading", "False", "Upgrading", "True", "Upgrading", 3), false, 0},
		{"degraded without an upgrade keeps its grace", upgradeDatabase("17", "17.6", "Degraded", "False", "NotHealthy", "", "", 3), true, databaseDegradedGrace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grace, failed := databaseKind.ready.failed(&tt.db)
			if failed != tt.wantFailed || (failed && grace != tt.wantGrace) {
				t.Errorf("failed() = %v, %v; want %v, %v", grace, failed, tt.wantGrace, tt.wantFailed)
			}
		})
	}
}

func TestDatabaseUpgradeNotice(t *testing.T) {
	held := upgradeDatabase("18", "17.6", "Running", "False", "UpgradeRolledBack", "False", "UpgradeRolledBack", 3)
	waiting := upgradeDatabase("18", "17.6", "Running", "False", "UpgradeWaiting", "True", "UpgradeWaiting", 3)
	upgrading := upgradeDatabase("18", "17.6", "Upgrading", "False", "Upgrading", "True", "Upgrading", 3)
	idle := upgradeDatabase("18", "18.1", "Running", "True", "Healthy", "False", "UpgradeSucceeded", 3)
	tests := []struct {
		name        string
		db          client.Database
		wantSummary string
		wantDetail  []string
	}{
		{"held", held, "database upgrade is on hold", []string{"rolled back", "`version = \"17\"`"}},
		{"waiting", waiting, "database upgrade in progress", []string{"(UpgradeWaiting)", "`version = \"17\"`"}},
		{"upgrading has no cancel", upgrading, "database upgrade in progress", []string{"(Upgrading)"}},
		{"nothing to say", idle, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, detail := databaseUpgradeNotice(&tt.db)
			if summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", summary, tt.wantSummary)
			}
			for _, want := range tt.wantDetail {
				if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(detail) {
					t.Errorf("detail %q lacks %q", detail, want)
				}
			}
		})
	}
	if _, detail := databaseUpgradeNotice(&upgrading); regexp.MustCompile("To cancel").MatchString(detail) {
		t.Errorf("an upgrade that is running offers a cancel: %q", detail)
	}
}
