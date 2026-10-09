package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// testAccDatabaseBindingConfig renders an unprotected database plus one
// binding with the given extra binding attributes.
func testAccDatabaseBindingConfig(host, cluster, namespace, role, extra string) string {
	return testAccDatabaseConfig(host, testDatabase{protection: boolPtr(false)}) + fmt.Sprintf(`
resource "kupe_database_binding" "test" {
  database  = kupe_database.test.name
  cluster   = %q
  namespace = %q
  role      = %q
%s
}
`, cluster, namespace, role, extra)
}

func TestAccDatabaseBindingResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database_binding.test"
	replace := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", "api", "app", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "orders-db-prod-api-app"),
					resource.TestCheckResourceAttr(addr, "database", "orders-db"),
					resource.TestCheckResourceAttr(addr, "secret_name", "orders-db-app"),
					resource.TestCheckResourceAttr(addr, "host", "orders-db.api.svc.cluster.local"),
					resource.TestCheckResourceAttr(addr, "phase", "Ready"),
				),
			},
			{
				ResourceName:                         addr,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "orders-db/orders-db-prod-api-app",
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// Bindings are immutable: a new namespace replaces the
				// binding, and the default name follows the new namespace.
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", "worker", "app", ""),
				ConfigPlanChecks: replace,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "orders-db-prod-worker-app"),
					resource.TestCheckResourceAttr(addr, "host", "orders-db.worker.svc.cluster.local"),
				),
			},
			{
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", "worker", "app", `  secret_name = "orders-credentials"`),
				ConfigPlanChecks: replace,
				Check:            resource.TestCheckResourceAttr(addr, "secret_name", "orders-credentials"),
			},
			{
				// An explicit name that differs from the current one
				// replaces the binding too.
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", "worker", "app",
					"  secret_name = \"orders-credentials\"\n  name = \"orders-worker\""),
				ConfigPlanChecks: replace,
				Check:            resource.TestCheckResourceAttr(addr, "name", "orders-worker"),
			},
			{
				// Removing name from config keeps the existing binding.
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", "worker", "app", `  secret_name = "orders-credentials"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)},
				},
				Check: resource.TestCheckResourceAttr(addr, "name", "orders-worker"),
			},
		},
	})
}

func TestAccDatabaseBindingResource_PlanTimeErrors(t *testing.T) {
	tests := []struct {
		name      string
		cluster   string
		namespace string
		role      string
		wantErr   string
	}{
		{name: "unknown role", cluster: "prod", namespace: "api", role: "admin", wantErr: `value must be one of`},
		{
			name: "default name longer than 63 characters", cluster: strings.Repeat("c", 30),
			namespace: strings.Repeat("n", 30), role: "app", wantErr: `Default binding name too long`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []resource.TestStep{
					{
						Config:      testAccDatabaseBindingConfig(mock.url(), tt.cluster, tt.namespace, tt.role, ""),
						ExpectError: regexp.MustCompile(tt.wantErr),
					},
				},
			})
		})
	}
}

func TestParseDatabaseBindingImportID(t *testing.T) {
	tests := []struct {
		id                     string
		wantDatabase, wantName string
		wantOK                 bool
	}{
		{id: "orders-db/orders-db-prod-api-app", wantDatabase: "orders-db", wantName: "orders-db-prod-api-app", wantOK: true},
		{id: "orders-db", wantOK: false},
		{id: "/binding", wantOK: false},
		{id: "orders-db/", wantOK: false},
		{id: "a/b/c", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			database, name, ok := parseDatabaseBindingImportID(tt.id)
			if ok != tt.wantOK || database != tt.wantDatabase || name != tt.wantName {
				t.Errorf("parseDatabaseBindingImportID(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.id, database, name, ok, tt.wantDatabase, tt.wantName, tt.wantOK)
			}
		})
	}
}
