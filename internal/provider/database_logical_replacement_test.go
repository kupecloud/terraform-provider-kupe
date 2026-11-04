package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func TestAccDatabaseResource_LogicalDeclaredInstanceReplacement(t *testing.T) {
	for _, tt := range []struct {
		name, list                     string
		unknown, omitPostgres, restore bool
		count, schemas                 int
	}{
		{"known new target", `[{name="invoices_db"},{name="analytics",schemas=[{name="reporting"}]}]`, false, false, false, 2, 1},
		{"known new target omitted schemas", `[{name="invoices_db"},{name="analytics"}]`, false, false, false, 2, 0},
		{"known new target null list", `null`, false, false, false, 1, 0},
		{"known new target omitted postgres", "", false, true, false, 1, 0},
		{"unknown new target known list", `[{name="invoices_db"},{name="analytics",schemas=[{name="reporting"}]}]`, true, false, false, 2, 1},
		{"unknown new target first name", `[{name=replace(terraform_data.instance.output,"-","_")},{name="analytics",schemas=[{name="reporting"}]}]`, true, false, false, 2, 1},
		{"unknown new target omitted postgres", "", true, true, false, 1, 0},
		{"restored replacement refuses list", `[{name="invoices_db"}]`, false, false, true, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			initial := logicalConfig(m, logicalTwo)
			next := logicalConfig(m, tt.list)
			if tt.omitPostgres {
				next = testAccDatabaseConfig(m.url(), testDatabase{protection: boolPtr(false)})
			}
			next = strings.Replace(next, `name = "orders-db"`, `name = "invoices-db"`, 1)
			if tt.unknown {
				next = strings.Replace(next, `name = "invoices-db"`, `name = terraform_data.instance.output`, 1) + `resource "terraform_data" "instance" {input="invoices-db"}`
			}
			if tt.restore {
				next = strings.Replace(next, `name = "invoices-db"`, `name = "invoices-db"`+"\n source={database=\"source-db\"}", 1)
			}
			step := resource.TestStep{Config: next}
			if tt.restore {
				step.PlanOnly = true
				step.ExpectError = regexp.MustCompile("Restore database settings after creation")
			} else {
				step.ConfigPlanChecks = logicalAction(plancheck.ResourceActionDestroyBeforeCreate)
				step.Check = resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database.test", "name", "invoices-db"), resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.0.name", "invoices_db"), resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.#", fmt.Sprint(tt.count)), testAccCheckDatabaseRequests(m, func(reqs []mockRequest) error {
					if len(reqs) != 2 || reqs[0].method != "POST" || reqs[1].method != "POST" {
						return fmt.Errorf("replacement must create a new instance, never PATCH old first: %v", reqs)
					}
					return nil
				}))
				if tt.count == 2 {
					step.Check = resource.ComposeAggregateTestCheckFunc(step.Check, resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.1.schemas.#", fmt.Sprint(tt.schemas)))
				}
			}
			steps := []resource.TestStep{{Config: initial}, step}
			if tt.restore {
				steps = append(steps, resource.TestStep{Config: initial, Check: testAccCheckDatabaseRequests(m, func(reqs []mockRequest) error {
					if len(reqs) != 1 {
						return fmt.Errorf("restore override replacement reached API: %v", reqs)
					}
					return nil
				})})
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: steps})
		})
	}
}

func TestAccDatabaseResource_LogicalResolvedInvalidInstanceName(t *testing.T) {
	for _, tt := range []struct{ name, input, expression string }{
		{"underscore", `"bad_name"`, `terraform_data.instance.output`},
		{"double hyphen", `"bad--name"`, `terraform_data.instance.output`},
		{"too long", `"` + strings.Repeat("a", 45) + `"`, `terraform_data.instance.output`},
		{"empty", `""`, `terraform_data.instance.output`},
		{"null", `"resolved"`, `terraform_data.instance.output == "resolved" ? null : "valid-db"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			cfg := testAccDatabaseConfig(m.url(), testDatabase{protection: boolPtr(false)})
			cfg = strings.Replace(cfg, `name = "orders-db"`, `name = `+tt.expression, 1)
			extra := `resource "terraform_data" "instance" {input=` + tt.input + `}`
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
				{Config: cfg + extra, ExpectError: regexp.MustCompile(`Invalid Attribute Value|Missing Configuration for Required Attribute|Database name unavailable`)},
				{Config: testAccProviderConfig(m.url()) + extra},
			}})
			m.mu.Lock()
			defer m.mu.Unlock()
			if len(m.databaseRequests) != 0 {
				t.Errorf("invalid resolved instance name reached POST/PATCH: %v", m.databaseRequests)
			}
		})
	}
}

func TestAccDatabaseResource_LogicalUnknownInstanceNameSame(t *testing.T) {
	for _, tt := range []struct {
		name, list string
		refuse     bool
	}{
		{"same logical list", logicalTwo, false},
		{"ordinary append", logicalThree, false},
		{"independent replacement resets list", `[{name="orders_db"}]`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			t.Cleanup(func() { m.mu.Lock(); defer m.mu.Unlock(); t.Logf("ACTUAL DATABASE WRITES: %v", m.databaseRequests) })
			initial := logicalConfig(m, logicalTwo)
			extra := `resource "terraform_data" "instance" {input="orders-db"}`
			next := strings.Replace(logicalConfig(m, tt.list), `name = "orders-db"`, `name = terraform_data.instance.output`, 1) + extra
			step := resource.TestStep{Config: next}
			if tt.refuse {
				step.ExpectError = regexp.MustCompile("cannot be removed")
			} else {
				step.ConfigPlanChecks = logicalAction(plancheck.ResourceActionDestroyBeforeCreate)
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: initial}, step}})
		})
	}
}
