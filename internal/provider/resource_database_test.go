package provider

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// testDatabase is the HCL a kupe_database.test config is rendered from.
// Empty strings and nil pointers leave the attribute out.
type testDatabase struct {
	name, engine, version        string
	cpu, memory, storage         string
	highAvailability, protection *bool
	createTimeout, updateTimeout string
	deleteTimeout                string
	// backup is the body of the `backup` attribute, e.g. "retention = 14";
	// "" renders `backup = {}`.
	backup *string
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func (d testDatabase) hcl() string {
	name, cpu, memory, storage := d.name, d.cpu, d.memory, d.storage
	if name == "" {
		name = "orders-db"
	}
	if cpu == "" {
		cpu = "500m"
	}
	if memory == "" {
		memory = "1Gi"
	}
	if storage == "" {
		storage = "10Gi"
	}
	attrs := ""
	if d.engine != "" {
		attrs += fmt.Sprintf("  engine = %q\n", d.engine)
	}
	if d.version != "" {
		attrs += fmt.Sprintf("  version = %q\n", d.version)
	}
	if d.highAvailability != nil {
		attrs += fmt.Sprintf("  high_availability = %t\n", *d.highAvailability)
	}
	if d.protection != nil {
		attrs += fmt.Sprintf("  deletion_protection = %t\n", *d.protection)
	}
	if d.backup != nil {
		attrs += fmt.Sprintf("  backup = { %s }\n", *d.backup)
	}
	var timeouts []string
	for _, t := range []struct{ op, value string }{
		{"create", d.createTimeout}, {"update", d.updateTimeout}, {"delete", d.deleteTimeout},
	} {
		if t.value != "" {
			timeouts = append(timeouts, fmt.Sprintf("%s = %q", t.op, t.value))
		}
	}
	if len(timeouts) > 0 {
		attrs += fmt.Sprintf("  timeouts = { %s }\n", strings.Join(timeouts, ", "))
	}
	return fmt.Sprintf(`
resource "kupe_database" "test" {
  name = %q
%s
  resources = {
    cpu     = %q
    memory  = %q
    storage = %q
  }
}
`, name, attrs, cpu, memory, storage)
}

func testAccProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}
`, host)
}

func testAccDatabaseConfig(host string, d testDatabase) string {
	return testAccProviderConfig(host) + d.hcl()
}

// testAccCheckNoDatabases verifies the mock holds no databases or bindings,
// i.e. destroy really deleted them through the API.
func testAccCheckNoDatabases(mock *mockKupeAPI) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.databases) != 0 || len(mock.databaseBindings) != 0 {
			return fmt.Errorf("expected no databases or bindings, got %d databases and %d bindings",
				len(mock.databases), len(mock.databaseBindings))
		}
		return nil
	}
}

func expectAction(action plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kupe_database.test", action)},
	}
}

func TestAccDatabaseResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database.test"
	retention14 := strPtr("retention = 14")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{backup: retention14}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "orders-db"),
					resource.TestCheckResourceAttr(addr, "engine", "postgres"),
					resource.TestCheckResourceAttr(addr, "version", "18"),
					resource.TestCheckResourceAttr(addr, "current_version", "18.0"),
					resource.TestCheckResourceAttr(addr, "phase", "Running"),
					resource.TestCheckResourceAttr(addr, "high_availability", "false"),
					resource.TestCheckResourceAttr(addr, "deletion_protection", "true"),
					resource.TestCheckResourceAttr(addr, "resources.storage", "10Gi"),
					resource.TestCheckResourceAttr(addr, "backup.retention", "14"),
					resource.TestCheckResourceAttr(addr, "backup.schedule", "Daily"),
					resource.TestCheckResourceAttr(addr, "last_backup_at", "2026-10-21T03:17:00Z"),
					resource.TestCheckResourceAttr(addr, "recovery_window.earliest", "2026-10-14T03:17:00Z"),
					resource.TestCheckResourceAttr(addr, "recovery_window.latest", "2026-10-21T09:00:00Z"),
					resource.TestCheckResourceAttr(addr, "backup_size_bytes", "5368709120"),
					resource.TestCheckResourceAttrSet(addr, "etag"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
				),
			},
			{
				Config:           testAccDatabaseConfig(mock.url(), testDatabase{cpu: "1", memory: "2Gi", storage: "20Gi", backup: retention14}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "resources.cpu", "1"),
					resource.TestCheckResourceAttr(addr, "resources.memory", "2Gi"),
					resource.TestCheckResourceAttr(addr, "resources.storage", "20Gi"),
					resource.TestCheckResourceAttr(addr, "version", "18"),
				),
			},
			{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{
					cpu: "1", memory: "2Gi", storage: "20Gi", highAvailability: boolPtr(true), backup: retention14,
				}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(addr, "high_availability", "true"),
			},
			{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{
					cpu: "1", memory: "2Gi", storage: "20Gi", highAvailability: boolPtr(false), backup: retention14,
				}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(addr, "high_availability", "false"),
			},
			{
				// The schedule changes in place; the retention is kept.
				Config: testAccDatabaseConfig(mock.url(), testDatabase{
					cpu: "1", memory: "2Gi", storage: "20Gi", highAvailability: boolPtr(false),
					backup: strPtr(`retention = 14, schedule = "DailyThenWeekly"`),
				}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "backup.retention", "14"),
					resource.TestCheckResourceAttr(addr, "backup.schedule", "DailyThenWeekly"),
				),
			},
			{
				// Removing `backup` keeps the settings: nothing to change.
				Config: testAccDatabaseConfig(mock.url(), testDatabase{
					cpu: "1", memory: "2Gi", storage: "20Gi", highAvailability: boolPtr(false),
				}),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "backup.retention", "14"),
					resource.TestCheckResourceAttr(addr, "backup.schedule", "DailyThenWeekly"),
				),
			},
			{
				// An unrelated change plans the recorded settings, not
				// "known after apply".
				Config: testAccDatabaseConfig(mock.url(), testDatabase{
					cpu: "2", memory: "2Gi", storage: "20Gi", highAvailability: boolPtr(false),
				}),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue(addr, tfjsonpath.New("backup"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"retention": knownvalue.Int64Exact(14),
						"schedule":  knownvalue.StringExact("DailyThenWeekly"),
					})),
				}},
				Check: resource.TestCheckResourceAttr(addr, "resources.cpu", "2"),
			},
			{
				ResourceName:                         addr,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "orders-db",
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// Lift protection so the test's final destroy succeeds.
				Config: testAccDatabaseConfig(mock.url(), testDatabase{
					cpu: "2", memory: "2Gi", storage: "20Gi", protection: boolPtr(false),
				}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(addr, "deletion_protection", "false"),
			},
		},
	})
}

func TestAccDatabaseResource_DeletionProtectionBlocksDestroy(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	refused := "(?s)deletion protection is enabled.*`deletion_protection\\s+=\\s+false`.*" + wrappedText(sharedDeleteNotice)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), testDatabase{})},
			{
				Config:      testAccProviderConfig(mock.url()),
				ExpectError: regexp.MustCompile(refused),
			},
			{
				// The refused delete left the database in state: lifting
				// protection is an in-place update, not a create.
				Config:           testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
			},
		},
	})
}

// sharedDeleteNotice is the wording for what deleting a database does to its
// data. Every Kupe surface (API docs, CLI, provider docs and diagnostics)
// states it verbatim.
const sharedDeleteNotice = "Deleting a database removes it and its automatic backups immediately, and they cannot be restored. " +
	"Manual backups stay until you delete them."

// wrappedText matches text in Terraform output, which may wrap it across lines.
func wrappedText(text string) string {
	words := strings.Fields(text)
	for i, word := range words {
		words[i] = regexp.QuoteMeta(word)
	}
	return strings.Join(words, `\s+`)
}

func TestDatabaseDeleteNotice(t *testing.T) {
	if databaseDeleteNotice != sharedDeleteNotice {
		t.Errorf("databaseDeleteNotice = %q, want the shared wording %q", databaseDeleteNotice, sharedDeleteNotice)
	}
	doc, err := os.ReadFile("../../docs/resources/database.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), sharedDeleteNotice) {
		t.Errorf("docs/resources/database.md does not state %q; run make docs", sharedDeleteNotice)
	}
}

// TestProtectionOnlyCoversEveryPatchField guards protectionOnly against the
// next PATCH field. A patch that sets deletion protection and anything else
// changes more than protection, so Update must wait for it. A field added to
// PatchDatabaseRequest that protectionOnly does not check fails here.
func TestProtectionOnlyCoversEveryPatchField(t *testing.T) {
	// exempt are the fields a patch that Update does not wait for may set.
	exempt := map[string]bool{"DeletionProtection": true}
	protected := true
	if !protectionOnly(client.PatchDatabaseRequest{DeletionProtection: &protected}) {
		t.Fatal("a patch of deletion protection alone is not protection only")
	}
	typ := reflect.TypeFor[client.PatchDatabaseRequest]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		if exempt[field.Name] {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			if field.Type.Kind() != reflect.Pointer {
				t.Fatalf("PatchDatabaseRequest.%s is a %s, not a pointer: protectionOnly cannot tell whether it is set",
					field.Name, field.Type)
			}
			patch := client.PatchDatabaseRequest{DeletionProtection: &protected}
			reflect.ValueOf(&patch).Elem().Field(i).Set(reflect.New(field.Type.Elem()))
			if protectionOnly(patch) {
				t.Errorf("protectionOnly ignores PatchDatabaseRequest.%s, so a patch that also sets it is not waited "+
					"for: check it in protectionOnly", field.Name)
			}
		})
	}
}

func TestAccDatabaseResource_PlanTimeErrors(t *testing.T) {
	tests := []struct {
		name    string
		initial testDatabase
		changed testDatabase
		wantErr string
	}{
		{
			name:    "engine change is an error, not a replacement",
			initial: testDatabase{engine: "postgres"},
			changed: testDatabase{engine: "mysql"},
			wantErr: `Database engine cannot be changed`,
		},
		{
			name:    "storage shrink is an error",
			initial: testDatabase{storage: "20Gi"},
			changed: testDatabase{storage: "10240Mi"},
			wantErr: `Storage cannot shrink`,
		},
		{
			name:    "version change is an error while major upgrades are unavailable",
			initial: testDatabase{version: "17"},
			changed: testDatabase{version: "18"},
			wantErr: `Major version upgrades are not available yet`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			tt.initial.protection = boolPtr(false)
			tt.changed.protection = boolPtr(false)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps: []resource.TestStep{
					{Config: testAccDatabaseConfig(mock.url(), tt.initial)},
					{
						Config:      testAccDatabaseConfig(mock.url(), tt.changed),
						ExpectError: regexp.MustCompile(tt.wantErr),
					},
				},
			})
		})
	}
}

// TestAccDatabaseResource_StorageGrowsWithOtherUnits guards against the
// shrink check comparing strings: 20Gi -> 25600Mi is growth.
func TestAccDatabaseResource_StorageGrowsWithOtherUnits(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), testDatabase{storage: "20Gi", protection: boolPtr(false)})},
			{
				Config:           testAccDatabaseConfig(mock.url(), testDatabase{storage: "25600Mi", protection: boolPtr(false)}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("kupe_database.test", "resources.storage", "25600Mi"),
			},
		},
	})
}

// TestAccDatabaseResource_DatabasesUnavailable: with managed databases
// disabled, kupe-api still serves reads, deletes and a PATCH of deletion
// protection alone. Refresh must keep the database in state, other changes
// must fail with a clear message, and the database must still be removable.
func TestAccDatabaseResource_DatabasesUnavailable(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), testDatabase{})},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.databasesDisabled = true }) },
				Config:           testAccDatabaseConfig(mock.url(), testDatabase{cpu: "1"}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				ExpectError:      regexp.MustCompile("Databases are not enabled"),
			},
			{
				Config:           testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("kupe_database.test", "deletion_protection", "false"),
			},
			{
				Config: testAccProviderConfig(mock.url()),
				Check:  testAccCheckNoDatabases(mock),
			},
		},
	})
}

// TestAccDatabaseResource_UpdateAfterConcurrentWrite covers a write landing
// between refresh and update, which makes the update's ETag stale.
func TestAccDatabaseResource_UpdateAfterConcurrentWrite(t *testing.T) {
	statusWrite := func(db map[string]any) {
		db["status"].(map[string]any)["phase"] = "Running"
	}
	specWrite := func(field string, value any) func(map[string]any) {
		return func(db map[string]any) {
			if field == "cpu" {
				db["resources"].(map[string]any)["cpu"] = value
			} else {
				db[field] = value
			}
			db["generation"] = db["generation"].(int) + 1
		}
	}
	tests := []struct {
		name        string
		beforePatch []func(map[string]any)
		wantErr     string // empty means the update applies
	}{
		{name: "a status write is retried with the fresh ETag", beforePatch: []func(map[string]any){statusWrite}},
		{
			name:        "the retry happens once",
			beforePatch: []func(map[string]any){statusWrite, statusWrite},
			wantErr:     `(?s)kupe API returned 412.*kept changing while the update was being sent`,
		},
		{
			name:        "a change to the patched setting is reported",
			beforePatch: []func(map[string]any){specWrite("cpu", "2")},
			wantErr:     `(?s)database changed outside this apply.*\(resources now differs`,
		},
		{
			name:        "a change to another setting is reported",
			beforePatch: []func(map[string]any){specWrite("highAvailability", true)},
			wantErr:     `(?s)database changed outside this apply.*\(high_availability now differs`,
		},
		{
			name:        "a change to the backup settings is reported",
			beforePatch: []func(map[string]any){specWrite("backup", map[string]any{"retention": float64(21), "schedule": "Daily"})},
			wantErr:     `(?s)database changed outside this apply.*\(backup now differs`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			step := resource.TestStep{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.databaseBeforePatch = tt.beforePatch })
				},
				Config: testAccDatabaseConfig(mock.url(), testDatabase{cpu: "1", protection: boolPtr(false)}),
				Check:  resource.TestCheckResourceAttr("kupe_database.test", "resources.cpu", "1"),
			}
			if tt.wantErr != "" {
				step.Check, step.ExpectError = nil, regexp.MustCompile(tt.wantErr)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps: []resource.TestStep{
					{Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})},
					step,
				},
			})
		})
	}
}

func TestAccDatabaseResource_CreationPaused(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) { m.creationPaused = true })

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config:      testAccDatabaseConfig(mock.url(), testDatabase{}),
				ExpectError: regexp.MustCompile(`(?s)Error code: creation_paused.*temporarily paused on the Kupe\s+platform,\s+so\s+nothing\s+was\s+created`),
			},
		},
	})
}

// TestAccDatabaseResource_DeleteTimesOutOnConflict: a delete the platform
// keeps refusing with a plain 409 ends in an error that says the delete was
// not accepted, with a hint about deleting rather than about name clashes.
func TestAccDatabaseResource_DeleteTimesOutOnConflict(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false), deleteTimeout: "1s"})},
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.databaseDeleteConflicts = math.MaxInt })
				},
				Config:      testAccProviderConfig(mock.url()),
				ExpectError: regexp.MustCompile(`(?s)delete not accepted before the timeout: kupe API returned 409.*kept refusing the delete`),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.databaseDeleteConflicts = 0 }) },
				Config:    testAccProviderConfig(mock.url()),
			},
		},
	})
}

// TestAccDatabaseResource_InvalidConfig: names and quantities the platform
// would refuse fail validation before anything is sent.
func TestAccDatabaseResource_InvalidConfig(t *testing.T) {
	const badName, badCPU, badSize = "must be lowercase letters", "must be a CPU count", "must be a number with a unit"
	tests := []struct {
		name    string
		db      testDatabase
		wantErr string
	}{
		{name: "uppercase name", db: testDatabase{name: "Orders"}, wantErr: badName},
		{name: "name starting with a digit", db: testDatabase{name: "1orders"}, wantErr: badName},
		{name: "name with a double hyphen", db: testDatabase{name: "orders--db"}, wantErr: badName},
		{name: "name ending with a hyphen", db: testDatabase{name: "orders-"}, wantErr: badName},
		{name: "name over the platform's 44 characters", db: testDatabase{name: "d" + strings.Repeat("b", 44)}, wantErr: "string length must be at most 44"},
		{name: "cpu with a unit", db: testDatabase{cpu: "1Gi"}, wantErr: badCPU},
		{name: "memory with milli and a unit", db: testDatabase{memory: "1mGi"}, wantErr: badSize},
		{name: "memory without a unit", db: testDatabase{memory: "1024"}, wantErr: badSize},
		{name: "storage with an unknown unit", db: testDatabase{storage: "10GB"}, wantErr: badSize},
		{name: "storage over 20 characters", db: testDatabase{storage: "1234567890123456789Gi"}, wantErr: "string length must be at most 20"},
		{
			name: "a backup schedule the platform does not offer", db: testDatabase{backup: strPtr(`schedule = "Weekly"`)},
			wantErr: `backup.schedule value must be one of:\s+\["Daily" "DailyThenWeekly"\],\s+got: "Weekly"`,
		},
		{
			name: "a backup retention of 0 days", db: testDatabase{backup: strPtr("retention = 0")},
			wantErr: `backup.retention value must be at least 1, got: 0`,
		},
		{
			name: "a backup retention beyond the API's int32", db: testDatabase{backup: strPtr("retention = 2147483648")},
			wantErr: `backup.retention value must be at most 2147483647, got: 2147483648`,
		},
		{
			name: "an empty backup", db: testDatabase{backup: strPtr("")},
			wantErr: wrappedText("At least one attribute out of [backup.schedule,backup.retention] must be specified"),
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
					{Config: testAccDatabaseConfig(mock.url(), tt.db), ExpectError: regexp.MustCompile(tt.wantErr)},
				},
			})
		})
	}
}

// TestAccDatabaseResource_NameAtThePlatformLimit: a 44-character name, the
// most any tenant may use, passes validation and is created.
func TestAccDatabaseResource_NameAtThePlatformLimit(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	name := "d" + strings.Repeat("b", 43)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{{
			Config: testAccDatabaseConfig(mock.url(), testDatabase{name: name, protection: boolPtr(false)}),
			Check:  resource.TestCheckResourceAttr("kupe_database.test", "name", name),
		}},
	})
}

func TestAccDatabaseResource_DeleteRetriesConflict(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.databaseDeleteConflicts = 2 }) },
				Config:    testAccProviderConfig(mock.url()),
				Check:     testAccCheckNoDatabases(mock),
			},
		},
	})
}

func TestAccDatabaseDataSource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "data.kupe_database.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}) + `
data "kupe_database" "test" {
  name = kupe_database.test.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "engine", "postgres"),
					resource.TestCheckResourceAttr(addr, "version", "18"),
					resource.TestCheckResourceAttr(addr, "current_version", "18.0"),
					resource.TestCheckResourceAttr(addr, "phase", "Running"),
					resource.TestCheckResourceAttr(addr, "resources.memory", "1Gi"),
					resource.TestCheckResourceAttr(addr, "deletion_protection", "false"),
					resource.TestCheckResourceAttr(addr, "backup.retention", "7"),
					resource.TestCheckResourceAttr(addr, "backup.schedule", "Daily"),
					resource.TestCheckResourceAttr(addr, "last_backup_at", "2026-10-21T03:17:00Z"),
					resource.TestCheckResourceAttr(addr, "recovery_window.earliest", "2026-10-14T03:17:00Z"),
					resource.TestCheckResourceAttr(addr, "recovery_window.latest", "2026-10-21T09:00:00Z"),
					resource.TestCheckResourceAttr(addr, "backup_size_bytes", "5368709120"),
				),
			},
		},
	})
}

func TestDatabaseConverged(t *testing.T) {
	ready := []client.Condition{{Type: "Ready", Status: "True"}}
	rolling := []client.Condition{{Type: "Ready", Status: "False", Message: "applying changes"}}
	tests := []struct {
		name string
		db   client.Database
		want bool
	}{
		{name: "no status yet", db: client.Database{Generation: 1}, want: false},
		{
			name: "provisioning",
			db:   client.Database{Generation: 1, Status: &client.DatabaseStatus{Phase: "Provisioning", ObservedGeneration: 1}},
			want: false,
		},
		{
			name: "running but the latest change is not applied",
			db:   client.Database{Generation: 3, Status: &client.DatabaseStatus{Phase: "Running", ObservedGeneration: 2, Conditions: ready}},
			want: false,
		},
		{
			name: "running with the change applied while the instances roll onto it",
			db:   client.Database{Generation: 3, Status: &client.DatabaseStatus{Phase: "Running", ObservedGeneration: 3, Conditions: rolling}},
			want: false,
		},
		{
			name: "running with the latest change in effect",
			db:   client.Database{Generation: 3, Status: &client.DatabaseStatus{Phase: "Running", ObservedGeneration: 3, Conditions: ready}},
			want: true,
		},
		{
			name: "degraded with a stale Ready condition",
			db:   client.Database{Generation: 3, Status: &client.DatabaseStatus{Phase: "Degraded", ObservedGeneration: 3, Conditions: ready}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseConverged(&tt.db); got != tt.want {
				t.Errorf("databaseConverged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseSizeQuantity(t *testing.T) {
	tests := []struct {
		a, b   string
		wantOK bool
		cmp    int // sign of a compared with b
	}{
		{a: "10Gi", b: "10240Mi", wantOK: true, cmp: 0},
		{a: "1.5Gi", b: "1536Mi", wantOK: true, cmp: 0},
		{a: "1G", b: "1Gi", wantOK: true, cmp: -1},
		{a: "0.5Ti", b: "512Gi", wantOK: true, cmp: 0},
		{a: "1mGi", b: "1Gi", wantOK: false},
		{a: ".5Ti", b: "1Gi", wantOK: false},
		{a: "500m", b: "1Gi", wantOK: false},
		{a: "1024", b: "1Gi", wantOK: false},
		{a: "10GB", b: "1Gi", wantOK: false},
		{a: "", b: "1Gi", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			a, okA := parseSizeQuantity(tt.a)
			b, okB := parseSizeQuantity(tt.b)
			if okA != tt.wantOK || !okB {
				t.Fatalf("parseSizeQuantity(%q) ok = %v, want %v (and %q ok = %v)", tt.a, okA, tt.wantOK, tt.b, okB)
			}
			if tt.wantOK && a.Cmp(b) != tt.cmp {
				t.Errorf("%s compared with %s = %d, want %d", tt.a, tt.b, a.Cmp(b), tt.cmp)
			}
		})
	}
}

func TestCPUQuantityPattern(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "2", want: true},
		{value: "500m", want: true},
		{value: "0.5", want: true},
		{value: ".5", want: true},
		{value: "1Gi", want: false},
		{value: "1mGi", want: false},
		{value: "1.5m", want: false},
		{value: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := cpuQuantityPattern.MatchString(tt.value); got != tt.want {
				t.Errorf("cpuQuantityPattern matches %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// testAccCheckDatabaseRequestBackup checks the backup the last database
// request of method (POST or PATCH) sent, as JSON with sorted keys; want ""
// means it sent none.
func testAccCheckDatabaseRequestBackup(mock *mockKupeAPI, method, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		var last map[string]any
		for _, req := range mock.databaseRequests {
			if req.method == method {
				last = req.body
			}
		}
		if last == nil {
			return fmt.Errorf("no database %s was sent", method)
		}
		got := ""
		if backup, sent := last["backup"]; sent {
			raw, err := json.Marshal(backup)
			if err != nil {
				return err
			}
			got = string(raw)
		}
		if got != want {
			return fmt.Errorf("the last database %s sent backup %q, want %q (body %v)", method, got, want, last)
		}
		return nil
	}
}

// TestAccDatabaseResource_BackupSettings: the provider sends only the backup
// keys that change, never an unchanged `backup` (kupe-api refuses any
// `backup` while the platform does not offer backups), and gives a database
// without backup settings the platform's values for the keys it leaves out.
func TestAccDatabaseResource_BackupSettings(t *testing.T) {
	const addr, dataAddr = "kupe_database.test", "data.kupe_database.test"
	const dataSource = `
data "kupe_database" "test" {
  name = kupe_database.test.name
}
`
	off := boolPtr(false)

	t.Run("a database without backup settings gains them", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		// Created while the platform did not offer backups, like a database
		// created before backups existed.
		mock.setState(func(m *mockKupeAPI) { m.backupsUnavailable = true })
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{
					Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: off}) + dataSource,
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckDatabaseRequestBackup(mock, http.MethodPost, ""),
						// backup is null, not an object of nulls.
						resource.TestCheckNoResourceAttr(addr, "backup.%"),
						resource.TestCheckNoResourceAttr(addr, "backup.retention"),
						resource.TestCheckNoResourceAttr(addr, "backup.schedule"),
						resource.TestCheckResourceAttr(addr, "last_backup_at", ""),
						resource.TestCheckNoResourceAttr(addr, "recovery_window.earliest"),
						resource.TestCheckResourceAttr(addr, "backup_size_bytes", "0"),
						resource.TestCheckNoResourceAttr(dataAddr, "backup.%"),
						resource.TestCheckNoResourceAttr(dataAddr, "backup.retention"),
						resource.TestCheckResourceAttr(dataAddr, "last_backup_at", ""),
						resource.TestCheckNoResourceAttr(dataAddr, "recovery_window.earliest"),
						resource.TestCheckResourceAttr(dataAddr, "backup_size_bytes", "0"),
					),
				},
				{
					// No `backup` is sent beside another change.
					Config:           testAccDatabaseConfig(mock.url(), testDatabase{protection: off, cpu: "1"}),
					ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckDatabaseRequestBackup(mock, http.MethodPatch, ""),
						resource.TestCheckNoResourceAttr(addr, "backup.retention"),
					),
				},
				{
					// Backups offered now: the schedule left out takes the
					// platform's default.
					PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.backupsUnavailable = false }) },
					Config:           testAccDatabaseConfig(mock.url(), testDatabase{protection: off, cpu: "1", backup: strPtr("retention = 14")}),
					ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckDatabaseRequestBackup(mock, http.MethodPatch, `{"retention":14}`),
						resource.TestCheckResourceAttr(addr, "backup.retention", "14"),
						resource.TestCheckResourceAttr(addr, "backup.schedule", "Daily"),
					),
				},
			},
		})
	})

	t.Run("a database without backup settings keeps a retention it never set", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		mock.setState(func(m *mockKupeAPI) { m.backupsUnavailable = true })
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: off})},
				{
					// The platform keeps no retention for it: the default
					// applies, and the next plan is empty.
					PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.backupsUnavailable = false }) },
					Config:           testAccDatabaseConfig(mock.url(), testDatabase{protection: off, backup: strPtr(`schedule = "DailyThenWeekly"`)}),
					ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckDatabaseRequestBackup(mock, http.MethodPatch, `{"schedule":"DailyThenWeekly"}`),
						resource.TestCheckNoResourceAttr(addr, "backup.retention"),
						resource.TestCheckResourceAttr(addr, "backup.schedule", "DailyThenWeekly"),
					),
				},
			},
		})
	})

	t.Run("only the backup keys that change are sent", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{
					Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: off, backup: strPtr("retention = 14")}),
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckDatabaseRequestBackup(mock, http.MethodPost, `{"retention":14}`),
						resource.TestCheckResourceAttr(addr, "backup.schedule", "Daily"),
					),
				},
				{
					Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: off, backup: strPtr("retention = 21")}),
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckDatabaseRequestBackup(mock, http.MethodPatch, `{"retention":21}`),
						resource.TestCheckResourceAttr(addr, "backup.retention", "21"),
						resource.TestCheckResourceAttr(addr, "backup.schedule", "Daily"),
					),
				},
				{
					Config: testAccDatabaseConfig(mock.url(), testDatabase{
						protection: off, backup: strPtr(`retention = 21, schedule = "DailyThenWeekly"`),
					}),
					Check: testAccCheckDatabaseRequestBackup(mock, http.MethodPatch, `{"schedule":"DailyThenWeekly"}`),
				},
				{
					Config: testAccDatabaseConfig(mock.url(), testDatabase{
						protection: off, cpu: "1", backup: strPtr(`retention = 21, schedule = "DailyThenWeekly"`),
					}),
					Check: testAccCheckDatabaseRequestBackup(mock, http.MethodPatch, ""),
				},
				{
					// kupe-api refuses any `backup` while the platform does not
					// offer backups: an unchanged one must stay off the wire.
					PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.backupsUnavailable = true }) },
					Config: testAccDatabaseConfig(mock.url(), testDatabase{
						protection: off, cpu: "2", backup: strPtr(`retention = 21, schedule = "DailyThenWeekly"`),
					}),
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckDatabaseRequestBackup(mock, http.MethodPatch, ""),
						resource.TestCheckResourceAttr(addr, "resources.cpu", "2"),
					),
				},
			},
		})
	})

	t.Run("a retention the platform refuses changes nothing", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		retention14 := testDatabase{protection: off, backup: strPtr("retention = 14")}
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{Config: testAccDatabaseConfig(mock.url(), retention14)},
				{
					Config: testAccDatabaseConfig(mock.url(), testDatabase{protection: off, backup: strPtr("retention = 36")}),
					ExpectError: regexp.MustCompile(`(?s)failed to update database.*` +
						wrappedText("backup.retention must be between 7 and 35 days") +
						`.*Error code: DATABASE_BACKUP_RETENTION_OUT_OF_RANGE\s+Field: spec.backup.retention`),
				},
				{
					Config:           testAccDatabaseConfig(mock.url(), retention14),
					ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
					Check:            resource.TestCheckResourceAttr(addr, "backup.retention", "14"),
				},
			},
		})
	})
}
