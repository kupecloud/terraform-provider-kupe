package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const testCopyAddr = "kupe_database.copy"

// testRestore is the HCL of kupe_database.copy, named orders-copy: source
// is the body of its `source` attribute ("" leaves `source` out), extra
// any other attributes.
func testRestore(source, extra string) string {
	sourceAttr := ""
	if source != "" {
		sourceAttr = fmt.Sprintf("  source = { %s }\n", source)
	}
	return fmt.Sprintf(`
resource "kupe_database" "copy" {
  name                = "orders-copy"
  deletion_protection = false
%s%s
  resources = {
    cpu     = "500m"
    memory  = "1Gi"
    storage = "10Gi"
  }
}
`, sourceAttr, extra)
}

// testAccRestoreConfig is an unprotected orders-db (version 17), a manual
// backup of it named nightly, and orders-copy restored from source.
func testAccRestoreConfig(host, source, extra string) string {
	return testAccRestoreSources(host) + testRestore(source, extra)
}

// testAccRestoreSources is orders-db (version 17) and its backup nightly.
func testAccRestoreSources(host string) string {
	return testAccDatabaseConfig(host, testDatabase{version: "17", protection: boolPtr(false)}) +
		testBackupHCL("kupe_database.test.name", `  name = "nightly"`)
}

// testAccCheckRestoreRequest checks the source and version the last
// database POST for orders-copy sent: want is the source as JSON, "" for
// none.
func testAccCheckRestoreRequest(mock *mockKupeAPI, wantSource string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		var last map[string]any
		for _, req := range mock.databaseRequests {
			if req.method == "POST" && req.body["name"] == "orders-copy" {
				last = req.body
			}
		}
		if last == nil {
			return fmt.Errorf("orders-copy was never created")
		}
		got := ""
		if source, sent := last["source"]; sent {
			raw, err := json.Marshal(source)
			if err != nil {
				return err
			}
			got = string(raw)
		}
		if got != wantSource {
			return fmt.Errorf("source sent = %s, want %s", got, wantSource)
		}
		// Unset in the configuration, the version is not sent, so the
		// platform runs the source's.
		if v, sent := last["version"]; sent && v != "" {
			return fmt.Errorf("version sent = %v, want none", v)
		}
		return nil
	}
}

// TestAccDatabaseResource_RestoreFromBackup: `source.backup` is sent on
// create and kept in state from the API's echo; the version is the
// backup's; removing `source` keeps the database; a restored database
// imports with its source.
func TestAccDatabaseResource_RestoreFromBackup(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	source := `backup = kupe_database_backup.test.name`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccRestoreConfig(mock.url(), source, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRestoreRequest(mock, `{"backup":"nightly"}`),
					resource.TestCheckResourceAttr(testCopyAddr, "source.backup", "nightly"),
					resource.TestCheckNoResourceAttr(testCopyAddr, "source.database"),
					resource.TestCheckNoResourceAttr(testCopyAddr, "source.point_in_time"),
					resource.TestCheckResourceAttr(testCopyAddr, "version", "17"),
					resource.TestCheckResourceAttr(testCopyAddr, "phase", "Running"),
				),
			},
			{
				// The echoed source and the source's version read back as
				// planned: nothing to do.
				Config:           testAccRestoreConfig(mock.url(), source, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// `source` applies only on create: removing it keeps the
				// database and what it was restored from.
				Config:           testAccRestoreConfig(mock.url(), "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(testCopyAddr, tfjsonpath.New("source").AtMapKey("backup"),
						knownvalue.StringExact("nightly")),
				},
			},
			{
				ResourceName:                         testCopyAddr,
				ImportState:                          true,
				ImportStateId:                        "orders-copy",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"timeouts", "etag"},
			},
		},
	})
}

// TestAccDatabaseResource_RestoreFromDatabase: `source.database` with a
// point in time is sent and kept; changing the source, or adding one to a
// database that was not restored, replaces the database.
func TestAccDatabaseResource_RestoreFromDatabase(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	at := func(point string) string {
		return fmt.Sprintf(`database = kupe_database.test.name, point_in_time = %q`, point)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccRestoreConfig(mock.url(), at("2026-10-20T09:00:00Z"), ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRestoreRequest(mock, `{"database":"orders-db","pointInTime":"2026-10-20T09:00:00Z"}`),
					resource.TestCheckResourceAttr(testCopyAddr, "source.database", "orders-db"),
					resource.TestCheckResourceAttr(testCopyAddr, "source.point_in_time", "2026-10-20T09:00:00Z"),
					resource.TestCheckNoResourceAttr(testCopyAddr, "source.backup"),
					resource.TestCheckResourceAttr(testCopyAddr, "version", "17"),
				),
			},
			{
				Config: testAccRestoreConfig(mock.url(), at("2026-10-19T09:00:00Z"), ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testCopyAddr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: testAccCheckRestoreRequest(mock, `{"database":"orders-db","pointInTime":"2026-10-19T09:00:00Z"}`),
			},
			{
				// The latest point: no point_in_time.
				Config: testAccRestoreConfig(mock.url(), `database = kupe_database.test.name`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testCopyAddr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRestoreRequest(mock, `{"database":"orders-db"}`),
					resource.TestCheckNoResourceAttr(testCopyAddr, "source.point_in_time"),
				),
			},
		},
	})
}

// TestAccDatabaseResource_RestoredStaysStable: on a restored database an
// unrelated change is an in-place update that plans the recorded version
// and source, with `source` in the configuration and after it is removed.
func TestAccDatabaseResource_RestoredStaysStable(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	src := `database = kupe_database.test.name, point_in_time = "2026-10-20T09:00:00Z"`
	withCPU := func(source, cpu string) string {
		return testAccRestoreSources(mock.url()) +
			strings.Replace(testRestore(source, ""), `cpu     = "500m"`, `cpu     = "`+cpu+`"`, 1)
	}
	kept := []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(testCopyAddr, plancheck.ResourceActionUpdate),
		plancheck.ExpectKnownValue(testCopyAddr, tfjsonpath.New("version"), knownvalue.StringExact("17")),
		plancheck.ExpectKnownValue(testCopyAddr, tfjsonpath.New("source").AtMapKey("point_in_time"),
			knownvalue.StringExact("2026-10-20T09:00:00Z")),
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{Config: withCPU(src, "500m")},
			{Config: withCPU(src, "1"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: kept}},
			{Config: withCPU("", "2"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: kept}},
			{
				Config:           withCPU("", "2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check:            resource.TestCheckResourceAttr(testCopyAddr, "version", "17"),
			},
		},
	})
}

// TestAccDatabaseResource_ProtectedRestoreReplaceRefused: a changed
// `source` plans a replacement of a protected restored database; the delete
// is refused, and the error says how to keep the database. A database that
// was not restored does not get that sentence.
func TestAccDatabaseResource_ProtectedRestoreReplaceRefused(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	protected := func(source string) string {
		return testAccRestoreSources(mock.url()) +
			strings.Replace(testRestore(source, ""), "deletion_protection = false", "deletion_protection = true", 1)
	}
	keep := wrappedText(restoredReplaceNotice)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{Config: protected(`backup = kupe_database_backup.test.name`)},
			{
				Config: protected(`database = kupe_database.test.name`),
				ExpectError: regexp.MustCompile(`(?s)database has deletion protection enabled.*` +
					wrappedText(sharedDeleteNotice) + `\s+` + keep),
			},
			{
				// Removing `source` keeps the database: nothing to do.
				Config:           protected(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{Config: testAccRestoreConfig(mock.url(), "", "")},
		},
	})
}

// TestDatabaseDeleteRefusedDetail: only a restored database's refusal says
// to remove `source`.
func TestDatabaseDeleteRefusedDetail(t *testing.T) {
	refusal := &client.APIError{StatusCode: http.StatusConflict, Code: client.CodeDeletionProtected, Message: "protected"}
	plain := DatabaseResourceModel{databaseModel: databaseModel{Name: types.StringValue("orders-db"),
		Source: types.ObjectNull(databaseSourceAttrTypes)}}
	restored := plain
	restored.Source = sourceFromAPI(&client.DatabaseSource{Backup: "nightly"})
	if _, detail, ok := databaseKind.deleteRefused(&plain, refusal); !ok || strings.Contains(detail, restoredReplaceNotice) {
		t.Errorf("plain database refusal = %q", detail)
	}
	if _, detail, ok := databaseKind.deleteRefused(&restored, refusal); !ok ||
		!strings.HasSuffix(detail, databaseDeleteNotice+" "+restoredReplaceNotice) {
		t.Errorf("restored database refusal = %q", detail)
	}
}

// TestAccDatabaseResource_SourceAddedReplaces: a database that was not
// restored is replaced when `source` is added: it cannot become a restore
// in place.
func TestAccDatabaseResource_SourceAddedReplaces(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccRestoreConfig(mock.url(), "", ""),
				// A new database without `source` plans none, not "known
				// after apply".
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectKnownValue(testCopyAddr, tfjsonpath.New("source"), knownvalue.Null()),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRestoreRequest(mock, ""),
					resource.TestCheckNoResourceAttr(testCopyAddr, "source"),
					resource.TestCheckResourceAttr(testCopyAddr, "version", "18"),
				),
			},
			{
				Config: testAccRestoreConfig(mock.url(), `backup = kupe_database_backup.test.name`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testCopyAddr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRestoreRequest(mock, `{"backup":"nightly"}`),
					resource.TestCheckResourceAttr(testCopyAddr, "version", "17"),
				),
			},
		},
	})
}

// TestAccDatabaseResource_RestoreRefusals: the platform's restore refusals
// end Create with their remedy.
func TestAccDatabaseResource_RestoreRefusals(t *testing.T) {
	tests := []struct {
		name, source, extra string
		setup               func(m *mockKupeAPI)
		wantErr             string
	}{
		{
			name:    "the version differs from the source's",
			source:  `backup = kupe_database_backup.test.name`,
			extra:   `  version = "18"`,
			wantErr: `(?s)Error code: restore_version_mismatch.*Field: spec.version.*` + wrappedText("Remove `version`, or set the source's version"),
		},
		{
			name:    "the backup does not exist",
			source:  `backup = "missing"`,
			wantErr: `(?s)Error code: restore_source_not_found.*` + wrappedText("named in `source` does not exist"),
		},
		{
			name:    "the point is outside the restore window",
			source:  `database = kupe_database.test.name, point_in_time = "2026-10-01T00:00:00Z"`,
			wantErr: `(?s)Error code: restore_point_outside_window.*` + wrappedText("outside the restore window"),
		},
		{
			name:    "less storage than the source",
			source:  `backup = kupe_database_backup.test.name`,
			setup:   func(m *mockKupeAPI) { m.backupSources["nightly"]["storage"] = "20Gi" },
			wantErr: `(?s)Error code: restore_storage_too_small.*` + wrappedText("at least the storage its source had"),
		},
		{
			name:    "the backup has not completed",
			source:  `backup = kupe_database_backup.test.name`,
			setup:   func(m *mockKupeAPI) { m.backupStatuses = []map[string]any{{"phase": "Running"}} },
			wantErr: `(?s)Error code: restore_source_not_ready.*` + wrappedText("has not completed"),
		},
		{
			name:    "backups off",
			source:  `backup = kupe_database_backup.test.name`,
			setup:   func(m *mockKupeAPI) { m.backupsUnavailable = true },
			wantErr: `(?s)Error code: backups_unavailable\s+Field: spec.source.*` + wrappedText("Remove `source` from the configuration"),
		},
		{
			name:    "no manual backups on the platform",
			source:  `backup = kupe_database_backup.test.name`,
			setup:   func(m *mockKupeAPI) { m.backupsNotServed = true },
			wantErr: `(?s)kupe API returned 404.*Error code: backups_unavailable.*` + wrappedText("does not offer manual backups or restores yet"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			setup := tt.setup
			if setup == nil {
				setup = func(*mockKupeAPI) {}
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoBackups(mock),
				Steps: []resource.TestStep{
					{Config: testAccRestoreSources(mock.url())},
					{
						PreConfig:   func() { mock.setState(setup) },
						Config:      testAccRestoreConfig(mock.url(), tt.source, tt.extra),
						ExpectError: regexp.MustCompile(`(?s)failed to create database.*` + tt.wantErr),
					},
				},
			})
		})
	}
}

func TestAccDatabaseResource_SourcePlanTimeErrors(t *testing.T) {
	tests := []struct {
		name, source, wantErr string
	}{
		{
			name:    "both backup and database",
			source:  `backup = "nightly", database = "orders-db"`,
			wantErr: `Invalid Attribute Combination`,
		},
		{
			name:    "neither backup nor database",
			source:  `point_in_time = "2026-10-20T09:00:00Z"`,
			wantErr: `Invalid Attribute Combination`,
		},
		{
			name:    "point_in_time with a backup",
			source:  `backup = "nightly", point_in_time = "2026-10-20T09:00:00Z"`,
			wantErr: wrappedText(`Attribute "source.database" must be specified when "source.point_in_time" is`),
		},
		{
			name:    "point_in_time not RFC 3339",
			source:  `database = "orders-db", point_in_time = "yesterday"`,
			wantErr: `(?s)Invalid point in time.*` + wrappedText("must be a time in UTC with whole seconds, for example 2026-10-30T09:00:00Z, got: yesterday"),
		},
		{
			name:    "point_in_time with an offset",
			source:  `database = "orders-db", point_in_time = "2026-10-20T10:00:00+01:00"`,
			wantErr: `Invalid point in time`,
		},
		{
			name:    "point_in_time with fractional seconds",
			source:  `database = "orders-db", point_in_time = "2026-10-20T09:00:00.5Z"`,
			wantErr: `Invalid point in time`,
		},
		{
			name:    "backup name not a DNS label",
			source:  `backup = "Nightly"`,
			wantErr: wrappedText(`start and end with a letter or digit, got: Nightly`),
		},
		{
			name:    "database name with --",
			source:  `database = "orders--db"`,
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
					Config:      testAccProviderConfig(mock.url()) + testRestore(tt.source, ""),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
		})
	}
}

func TestAccDatabaseDataSource_Source(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "data.kupe_database.copy"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoBackups(mock),
		Steps: []resource.TestStep{{
			Config: testAccRestoreConfig(mock.url(), `database = kupe_database.test.name, point_in_time = "2026-10-20T09:00:00Z"`, "") + `
data "kupe_database" "copy" {
  name = kupe_database.copy.name
}

data "kupe_database" "plain" {
  name = kupe_database.test.name
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "source.database", "orders-db"),
				resource.TestCheckResourceAttr(addr, "source.point_in_time", "2026-10-20T09:00:00Z"),
				resource.TestCheckNoResourceAttr(addr, "source.backup"),
				resource.TestCheckResourceAttr(addr, "version", "17"),
				resource.TestCheckNoResourceAttr("data.kupe_database.plain", "source"),
			),
		}},
	})
}

// TestMapDatabaseSource: what the API echoes round-trips through state and
// back to the create request.
func TestMapDatabaseSource(t *testing.T) {
	tests := []struct {
		name string
		api  *client.DatabaseSource
		want map[string]attr.Value // nil: a null object
	}{
		{name: "not restored", api: nil},
		{
			name: "from a backup",
			api:  &client.DatabaseSource{Backup: "nightly"},
			want: map[string]attr.Value{
				"backup": types.StringValue("nightly"), "database": types.StringNull(), "point_in_time": types.StringNull(),
			},
		},
		{
			name: "from a database at a point",
			api:  &client.DatabaseSource{Database: "orders-db", PointInTime: "2026-10-20T09:00:00Z"},
			want: map[string]attr.Value{
				"backup": types.StringNull(), "database": types.StringValue("orders-db"),
				"point_in_time": types.StringValue("2026-10-20T09:00:00Z"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := databaseModelFromAPI(&client.Database{Name: "orders-copy", Source: tt.api}).Source
			want := types.ObjectNull(databaseSourceAttrTypes)
			if tt.want != nil {
				want = types.ObjectValueMust(databaseSourceAttrTypes, tt.want)
			}
			if !got.Equal(want) {
				t.Fatalf("source = %v, want %v", got, want)
			}
			back := sourceOnCreate(got)
			if (back == nil) != (tt.api == nil) || (back != nil && *back != *tt.api) {
				t.Errorf("sourceOnCreate = %+v, want %+v", back, tt.api)
			}
		})
	}
}
