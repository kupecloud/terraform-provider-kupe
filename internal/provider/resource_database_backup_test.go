package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const testBackupAddr = "kupe_database_backup.test"

// testAccBackupConfig renders an unprotected database plus one backup of it
// with the given extra backup attributes.
func testAccBackupConfig(host, extra string) string {
	return testAccDatabaseConfig(host, testDatabase{protection: boolPtr(false)}) + testBackupHCL(
		"kupe_database.test.name", extra)
}

// testBackupHCL is a kupe_database_backup.test of database (an HCL
// expression) with extra attributes.
func testBackupHCL(database, extra string) string {
	return fmt.Sprintf(`
resource "kupe_database_backup" "test" {
  database = %s
%s
}
`, database, extra)
}

// testAccCheckNoBackups verifies the mock holds no backups, databases or
// bindings: destroy deleted them all through the API.
func testAccCheckNoBackups(mock *mockKupeAPI) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		mock.mu.Lock()
		count := len(mock.backups)
		mock.mu.Unlock()
		if count != 0 {
			return fmt.Errorf("expected no backups, got %d", count)
		}
		return testAccCheckNoDatabases(mock)(s)
	}
}

// testAccCheckBackupNamesSent checks the names the backup POSTs sent, in
// order; "" is a POST without a name.
func testAccCheckBackupNamesSent(mock *mockKupeAPI, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		got := make([]string, 0, len(mock.backupRequests))
		for _, req := range mock.backupRequests {
			name, set := req.body["name"]
			if set && name == "" {
				return fmt.Errorf("a backup POST sent an empty name: %v", req.body)
			}
			got = append(got, strOrEmpty(name))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("backup names sent = %q, want %q", got, want)
		}
		return nil
	}
}

// TestAccDatabaseBackupResource: create waits until the backup completes,
// the platform's name is kept, import by name, a `timeouts` change is in
// place, and destroy deletes the backup.
func TestAccDatabaseBackupResource(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) {
		m.backupStatuses = []map[string]any{
			{"phase": "Pending", "completedAt": "", "sizeBytes": 0, "source": nil, "conditions": []any{}},
			{"phase": "Running", "completedAt": "", "sizeBytes": 0},
			{},
		}
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccBackupConfig(mock.url(), ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testBackupAddr, "name", "orders-db-"+mockBackupTime),
					resource.TestCheckResourceAttr(testBackupAddr, "database", "orders-db"),
					resource.TestCheckResourceAttr(testBackupAddr, "phase", "Completed"),
					resource.TestCheckResourceAttr(testBackupAddr, "completed_at", "2026-10-30T14:17:00Z"),
					resource.TestCheckResourceAttr(testBackupAddr, "size_bytes", "1073741824"),
					resource.TestCheckResourceAttr(testBackupAddr, "source_version", "18"),
					resource.TestCheckResourceAttr(testBackupAddr, "source_storage", "10Gi"),
					resource.TestCheckResourceAttr(testBackupAddr, "database_deleted", "false"),
					resource.TestCheckResourceAttr(testBackupAddr, "created_at", "2026-10-30T14:15:00Z"),
					testAccCheckBackupNamesSent(mock, ""),
					func(*terraform.State) error {
						mock.mu.Lock()
						defer mock.mu.Unlock()
						if mock.backupGets < 3 {
							return fmt.Errorf("backup GETs = %d, want the create to wait through Pending and Running", mock.backupGets)
						}
						return nil
					},
				),
			},
			{
				// The name the platform chose stays: nothing to do.
				Config:           testAccBackupConfig(mock.url(), ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				ResourceName:                         testBackupAddr,
				ImportState:                          true,
				ImportStateId:                        "orders-db-" + mockBackupTime,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"timeouts"},
			},
			{
				Config: testAccBackupConfig(mock.url(), `  timeouts = { create = "90m" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testBackupAddr, plancheck.ResourceActionUpdate),
				}},
				Check: testAccCheckBackupNamesSent(mock, ""),
			},
		},
	})
}

// TestAccDatabaseBackupResource_NameReplaces: a named backup sends its name,
// and a new name takes a new backup: the old one is deleted.
func TestAccDatabaseBackupResource_NameReplaces(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccBackupConfig(mock.url(), `  name = "before-migration"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testBackupAddr, "name", "before-migration"),
					testAccCheckBackupNamesSent(mock, "before-migration"),
				),
			},
			{
				Config: testAccBackupConfig(mock.url(), `  name = "after-migration"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testBackupAddr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBackupNamesSent(mock, "before-migration", "after-migration"),
					func(*terraform.State) error {
						mock.mu.Lock()
						defer mock.mu.Unlock()
						if _, ok := mock.backups["before-migration"]; ok || len(mock.backups) != 1 {
							return fmt.Errorf("backups = %v, want only after-migration", mock.backups)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDatabaseBackupResource_Failed: a Failed backup ends Create at once
// with the platform's message and stays in state, tainted, so the next
// apply takes a new one.
func TestAccDatabaseBackupResource_Failed(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) {
		m.backupStatuses = []map[string]any{{
			"phase": "Failed", "completedAt": "",
			"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "Failed",
				"message": "the backup did not finish; take a new one"}},
		}}
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{
				// A short timeout: Failed must end the wait, not the timeout.
				Config: testAccBackupConfig(mock.url(), "  name = \"nightly\"\n  timeouts = { create = \"30s\" }"),
				ExpectError: regexp.MustCompile(`(?s)database backup failed.*` +
					wrappedText(`Backup "nightly" of database "orders-db" failed.`) + `.*` +
					wrappedText("Status message: the backup did not finish; take a new one") + `.*` +
					wrappedText("marked tainted")),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.backupStatuses = nil }) },
				Config:    testAccBackupConfig(mock.url(), `  name = "nightly"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testBackupAddr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr(testBackupAddr, "phase", "Completed"),
			},
		},
	})
}

// TestAccDatabaseBackupResource_SurvivesDatabaseDestroy: destroying the
// database leaves its manual backup, which reports the database deleted
// (D-9). A destroy of both deletes the backup too.
func TestAccDatabaseBackupResource_SurvivesDatabaseDestroy(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	backupOnly := testAccProviderConfig(mock.url()) + testBackupHCL(`"orders-db"`, `  name = "keep-me"`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{Config: testAccBackupConfig(mock.url(), `  name = "keep-me"`)},
			{
				// The database leaves the configuration; the backup names it
				// by value now and is not replaced.
				Config: backupOnly,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("kupe_database.test", plancheck.ResourceActionDestroy),
					plancheck.ExpectResourceAction(testBackupAddr, plancheck.ResourceActionNoop),
				}},
				Check: func(*terraform.State) error {
					mock.mu.Lock()
					defer mock.mu.Unlock()
					if len(mock.databases) != 0 {
						return fmt.Errorf("databases = %v, want none", mock.databases)
					}
					if _, ok := mock.backups["keep-me"]; !ok {
						return fmt.Errorf("the backup went with its database")
					}
					return nil
				},
			},
			{
				// A refresh reads the backup, now of a deleted database.
				Config: backupOnly,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testBackupAddr, "database_deleted", "true"),
					resource.TestCheckResourceAttr(testBackupAddr, "phase", "Completed"),
				),
			},
		},
	})
}

// TestAccDatabaseBackupResource_DeletedOutsideTerraform: a backup deleted
// elsewhere leaves state on refresh, so the next apply takes it again.
func TestAccDatabaseBackupResource_DeletedOutsideTerraform(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{Config: testAccBackupConfig(mock.url(), `  name = "nightly"`)},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { delete(m.backups, "nightly") }) },
				Config:    testAccBackupConfig(mock.url(), `  name = "nightly"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testBackupAddr, plancheck.ResourceActionCreate),
				}},
				Check: testAccCheckBackupNamesSent(mock, "nightly", "nightly"),
			},
		},
	})
}

// TestAccDatabaseBackupResource_DestroyWhileDatabasesOff: with databases
// switched off, a backup still refreshes and destroys (PD-27).
func TestAccDatabaseBackupResource_DestroyWhileDatabasesOff(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	backupOnly := testAccProviderConfig(mock.url()) + testBackupHCL(`"orders-db"`, `  name = "nightly"`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{Config: testAccBackupConfig(mock.url(), `  name = "nightly"`)},
			{Config: backupOnly},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.databasesDisabled = true }) },
				Config:    backupOnly,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
			{
				Config: testAccProviderConfig(mock.url()),
				Check: func(*terraform.State) error {
					mock.mu.Lock()
					defer mock.mu.Unlock()
					if len(mock.backups) != 0 {
						return fmt.Errorf("backups = %v, want none", mock.backups)
					}
					return nil
				},
			},
		},
	})
}

// TestAccDatabaseBackupResource_Refusals: refusals end Create with their
// remedy, and nothing is left in state.
func TestAccDatabaseBackupResource_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(m *mockKupeAPI)
		wantErr string
	}{
		{
			name:  "backups off",
			setup: func(m *mockKupeAPI) { m.backupsUnavailable = true },
			wantErr: `(?s)failed to create database backup.*Error code: backups_unavailable.*` +
				wrappedText("so no backup was taken. Remove the `kupe_database_backup` resource"),
		},
		{
			name:  "no manual backups on the platform",
			setup: func(m *mockKupeAPI) { m.backupsNotServed = true },
			wantErr: `(?s)failed to create database backup.*kupe API returned 404.*Error code: backups_unavailable.*` +
				wrappedText("does not offer manual backups or restores yet"),
		},
		{
			name: "limit reached",
			setup: func(m *mockKupeAPI) {
				for i := range mockBackupLimit {
					name := fmt.Sprintf("old-%d", i)
					m.backups[name] = map[string]any{"name": name, "database": "orders-db"}
				}
			},
			wantErr: `(?s)Error code: backup_limit_reached.*` + wrappedText("keeps 20 manual backups"),
		},
		{
			name: "database not running",
			setup: func(m *mockKupeAPI) {
				m.databaseStatuses = []map[string]any{{"phase": "Degraded"}}
			},
			wantErr: `(?s)Error code: database_not_ready.*` + wrappedText("The database is not running, so no backup was taken."),
		},
		{
			name: "database being deleted",
			setup: func(m *mockKupeAPI) {
				m.backupRefusal = &mockRefusal{http.StatusConflict, client.CodeTerminating, `database "orders-db" is being deleted`}
			},
			wantErr: `(?s)Error code: terminating.*` + wrappedText("The database is being deleted, so it cannot be backed up."),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps: []resource.TestStep{
					{Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})},
					{
						PreConfig:   func() { mock.setState(tt.setup) },
						Config:      testAccBackupConfig(mock.url(), ""),
						ExpectError: regexp.MustCompile(tt.wantErr),
					},
				},
			})
		})
	}
}

func TestAccDatabaseBackupResource_PlanTimeErrors(t *testing.T) {
	tests := []struct {
		name, config, wantErr string
	}{
		{
			name: "database is required",
			config: `
resource "kupe_database_backup" "test" {
  name = "nightly"
}
`,
			wantErr: `The argument "database" is required`,
		},
		{
			name:    "name must be a DNS label",
			config:  testBackupHCL(`"orders-db"`, `  name = "Nightly_1"`),
			wantErr: wrappedText(`start and end with a letter or digit, got: Nightly_1`),
		},
		{
			name:    "name at most 63 characters",
			config:  testBackupHCL(`"orders-db"`, fmt.Sprintf("  name = %q", strings.Repeat("a", 64))),
			wantErr: `string length must be at most 63`,
		},
		{
			name:    "database must be a database name",
			config:  testBackupHCL(`"orders--db"`, ""),
			wantErr: `not contain '--'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []resource.TestStep{{
					Config:      testAccProviderConfig(mock.url()) + tt.config,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
		})
	}
}

func TestAccDatabaseBackupDataSource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "data.kupe_database_backup.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccBackupConfig(mock.url(), `  name = "nightly"`) + `
data "kupe_database_backup" "test" {
  name = kupe_database_backup.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "database", "orders-db"),
					resource.TestCheckResourceAttr(addr, "phase", "Completed"),
					resource.TestCheckResourceAttr(addr, "completed_at", "2026-10-30T14:17:00Z"),
					resource.TestCheckResourceAttr(addr, "size_bytes", "1073741824"),
					resource.TestCheckResourceAttr(addr, "source_version", "18"),
					resource.TestCheckResourceAttr(addr, "source_storage", "10Gi"),
					resource.TestCheckResourceAttr(addr, "database_deleted", "false"),
					resource.TestCheckResourceAttr(addr, "created_at", "2026-10-30T14:15:00Z"),
				),
			},
			{
				Config: testAccProviderConfig(mock.url()) + `
data "kupe_database_backup" "test" {
  name = "missing"
}
`,
				ExpectError: regexp.MustCompile(`(?s)failed to read database backup.*kupe API returned 404`),
			},
		},
	})
}

func TestDatabaseBackupModelFromAPI(t *testing.T) {
	// Before the backup starts it has no source: the source fields are
	// empty, not null, so the plan never sees them change to unknown.
	m := databaseBackupModelFromAPI(&client.Backup{Name: "nightly", Database: "orders-db",
		Status: &client.BackupStatus{Phase: "Pending"}})
	if m.SourceVersion.ValueString() != "" || m.SourceVersion.IsNull() || m.SourceStorage.IsNull() || m.SizeBytes.ValueInt64() != 0 {
		t.Errorf("pending backup = %+v", m)
	}
	m = databaseBackupModelFromAPI(&client.Backup{Name: "nightly"})
	if m.Phase.ValueString() != "" || m.DatabaseDeleted.ValueBool() {
		t.Errorf("backup without status = %+v", m)
	}
}
