package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// testDatabase is the HCL a kupe_database.test config is rendered from.
// Empty strings and nil pointers leave the attribute out.
type testDatabase struct {
	engine, version              string
	cpu, memory, storage         string
	highAvailability, protection *bool
}

func boolPtr(b bool) *bool { return &b }

func (d testDatabase) hcl() string {
	cpu, memory, storage := d.cpu, d.memory, d.storage
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
	return fmt.Sprintf(`
resource "kupe_database" "test" {
  name = "orders-db"
%s
  resources = {
    cpu     = %q
    memory  = %q
    storage = %q
  }
}
`, attrs, cpu, memory, storage)
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

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "orders-db"),
					resource.TestCheckResourceAttr(addr, "engine", "postgres"),
					resource.TestCheckResourceAttr(addr, "version", "18"),
					resource.TestCheckResourceAttr(addr, "current_version", "18.0"),
					resource.TestCheckResourceAttr(addr, "phase", "Running"),
					resource.TestCheckResourceAttr(addr, "high_availability", "false"),
					resource.TestCheckResourceAttr(addr, "deletion_protection", "true"),
					resource.TestCheckResourceAttr(addr, "resources.storage", "10Gi"),
					resource.TestCheckResourceAttrSet(addr, "etag"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
				),
			},
			{
				Config:           testAccDatabaseConfig(mock.url(), testDatabase{cpu: "1", memory: "2Gi", storage: "20Gi"}),
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
					cpu: "1", memory: "2Gi", storage: "20Gi", highAvailability: boolPtr(true),
				}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(addr, "high_availability", "true"),
			},
			{
				Config: testAccDatabaseConfig(mock.url(), testDatabase{
					cpu: "1", memory: "2Gi", storage: "20Gi", highAvailability: boolPtr(false),
				}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(addr, "high_availability", "false"),
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
					cpu: "1", memory: "2Gi", storage: "20Gi", protection: boolPtr(false),
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

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseConfig(mock.url(), testDatabase{})},
			{
				Config:      testAccProviderConfig(mock.url()),
				ExpectError: regexp.MustCompile("(?s)deletion protection is enabled.*`deletion_protection\\s+=\\s+false`"),
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

// TestAccDatabaseResource_DatabasesUnavailableKeepsState: when the platform
// answers 404 databases_unavailable, refresh must fail with a clear message
// rather than treat the database as deleted and plan to create it again.
func TestAccDatabaseResource_DatabasesUnavailableKeepsState(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	config := testAccDatabaseConfig(mock.url(), testDatabase{})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:   func() { mock.setDatabaseState(func(m *mockKupeAPI) { m.databasesDisabled = true }) },
				Config:      config,
				ExpectError: regexp.MustCompile("Managed databases are not enabled"),
			},
			{
				PreConfig:        func() { mock.setDatabaseState(func(m *mockKupeAPI) { m.databasesDisabled = false }) },
				Config:           testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
			},
		},
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
				PreConfig: func() { mock.setDatabaseState(func(m *mockKupeAPI) { m.databaseDeleteConflicts = 2 }) },
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
				),
			},
		},
	})
}

func TestDatabaseConverged(t *testing.T) {
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
			db:   client.Database{Generation: 3, Status: &client.DatabaseStatus{Phase: "Running", ObservedGeneration: 2}},
			want: false,
		},
		{
			name: "running with the latest change applied",
			db:   client.Database{Generation: 3, Status: &client.DatabaseStatus{Phase: "Running", ObservedGeneration: 3}},
			want: true,
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

func TestParseQuantity(t *testing.T) {
	tests := []struct {
		a, b   string
		wantOK bool
		cmp    int // sign of a compared with b
	}{
		{a: "10Gi", b: "10240Mi", wantOK: true, cmp: 0},
		{a: "1.5Gi", b: "1536Mi", wantOK: true, cmp: 0},
		{a: "1G", b: "1Gi", wantOK: true, cmp: -1},
		{a: "500m", b: "1", wantOK: true, cmp: -1},
		{a: ".5Ti", b: "512Gi", wantOK: true, cmp: 0},
		{a: "10GB", b: "1Gi", wantOK: false},
		{a: "", b: "1Gi", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			a, okA := parseQuantity(tt.a)
			b, okB := parseQuantity(tt.b)
			if okA != tt.wantOK || !okB {
				t.Fatalf("parseQuantity(%q) ok = %v, want %v (and %q ok = %v)", tt.a, okA, tt.wantOK, tt.b, okB)
			}
			if tt.wantOK && a.Cmp(b) != tt.cmp {
				t.Errorf("%s compared with %s = %d, want %d", tt.a, tt.b, a.Cmp(b), tt.cmp)
			}
		})
	}
}
