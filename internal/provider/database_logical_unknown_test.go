package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccDatabaseResource_LogicalUnknownPlacements(t *testing.T) {
	const first = `{name="orders_db"}`
	const analytics = `{name="analytics",schemas=[{name="reporting"}]}`
	const twoSchemas = `[{name="orders_db"},{name="analytics",schemas=[{name="reporting"},{name="events"}]}]`
	tests := []struct {
		name, initial, list, input string
		refuse, planOnly, restored bool
	}{
		{"whole databases list", "", `terraform_data.late.output`, logicalTwo, false, false, false},
		{"database entry", "", `[` + first + `,terraform_data.late.output]`, analytics, false, false, false},
		{"database name", "", `[` + first + `,{name=terraform_data.late.output,schemas=[{name="reporting"}]}]`, `"analytics"`, false, false, false},
		{"schema list", "", `[` + first + `,{name="analytics",schemas=terraform_data.late.output}]`, `[{name="reporting"}]`, false, false, false},
		{"schema entry", "", `[` + first + `,{name="analytics",schemas=[terraform_data.late.output]}]`, `{name="reporting"}`, false, false, false},
		{"schema name", "", `[` + first + `,{name="analytics",schemas=[{name=terraform_data.late.output}]}]`, `"reporting"`, false, false, false},
		{"mixed known and unknown schema addition", "", `[` + first + `,{name="analytics",schemas=[{name="reporting"},{name=terraform_data.late.output}]}]`, `"events"`, false, false, false},
		{"reorder unknown database name", logicalThree, `[` + first + `,{name="warehouse"},{name=terraform_data.late.output}]`, `"analytics"`, false, false, false},
		{"first name", "", `[{name=terraform_data.late.output},` + analytics + `]`, `"orders_db"`, false, false, false},
		{"restored first name", "", `[{name=terraform_data.late.output},` + analytics + `]`, `"historical_db"`, false, false, true},
		{"whole list resolves removal", "", `terraform_data.late.output`, `[` + first + `]`, true, false, false},
		{"entry resolves rename", "", `[` + first + `,terraform_data.late.output]`, `{name="renamed"}`, true, false, false},
		{"name resolves rename", "", `[` + first + `,{name=terraform_data.late.output,schemas=[{name="reporting"}]}]`, `"renamed"`, true, false, false},
		{"schema list resolves removal", "", `[` + first + `,{name="analytics",schemas=terraform_data.late.output}]`, `[]`, true, false, false},
		{"schema entry resolves rename", "", `[` + first + `,{name="analytics",schemas=[terraform_data.late.output]}]`, `{name="renamed"}`, true, false, false},
		{"schema name resolves rename", "", `[` + first + `,{name="analytics",schemas=[{name=terraform_data.late.output}]}]`, `"renamed"`, true, false, false},
		{"first resolves change", "", `[{name=terraform_data.late.output},` + analytics + `]`, `"renamed"`, true, false, false},
		{"known removal cannot hide behind unknown", logicalThree, `[` + first + `,{name="new_db"},{name=terraform_data.late.output}]`, `"warehouse"`, true, true, false},
		{"known schema removal cannot hide behind unknown", twoSchemas, `[` + first + `,{name="analytics",schemas=[{name="new_schema"},{name=terraform_data.late.output}]}]`, `"reporting"`, true, true, false},
		{"known first change with unknown extra", "", `[{name="wrong"},` + analytics + `,{name=terraform_data.late.output}]`, `"extra"`, true, true, false},
		{"known first moved after unknown", "", `[{name=terraform_data.late.output},` + first + `,` + analytics + `]`, `"extra"`, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			initial := tt.initial
			if initial == "" {
				initial = logicalTwo
			}
			next := resource.TestStep{Config: logicalConfig(m, tt.list) + `resource "terraform_data" "late" {input=` + tt.input + `}`, PlanOnly: tt.planOnly}
			if tt.restored {
				next.PreConfig = func() {
					m.setState(func(m *mockKupeAPI) {
						db := m.databases["orders-db"]
						db["firstLogicalDatabase"] = "historical_db"
						p := db["postgres"].(map[string]any)
						list := p["databases"].([]any)
						list[0].(map[string]any)["name"] = "historical_db"
					})
				}
			}
			if tt.refuse {
				next.ExpectError = regexp.MustCompile(`cannot be removed|first entry must be`)
			}

			steps := []resource.TestStep{{Config: logicalConfig(m, initial)}, next}
			if tt.refuse {
				steps = append(steps, resource.TestStep{Config: logicalConfig(m, initial), Check: testAccCheckDatabaseRequests(m, func(reqs []mockRequest) error {
					if len(reqs) != 1 {
						return fmt.Errorf("refused unknown removal reached API: %v", reqs)
					}
					return nil
				})})
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: steps})
		})
	}
}
func TestAccDatabaseResource_LogicalAppendUnknownAdjacentLists(t *testing.T) {
	m := newMockKupeAPI()
	defer m.close()
	shrinkPollInterval(t)
	cfg := logicalConfig(m, logicalThree)
	cfg = strings.Replace(cfg, `roles = [{name="reporting"}]`, `roles = terraform_data.roles.output`, 1)
	cfg = strings.Replace(cfg, `extensions=[{name="pgcrypto"}]`, `extensions=terraform_data.extensions.output`, 1)
	cfg += `resource "terraform_data" "roles" {input=[{name="reporting"}]}` + "\n" + `resource "terraform_data" "extensions" {input=[{name="pgcrypto"}]}`
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: logicalConfig(m, logicalTwo)},
		{Config: cfg, Check: func(s *terraform.State) error {
			if err := logicalNoPasswordState(s); err != nil {
				return err
			}
			return testAccCheckDatabaseRequests(m, func(reqs []mockRequest) error {
				last := reqs[len(reqs)-1]
				p, _ := last.body["postgres"].(map[string]any)
				if last.method != "PATCH" || len(p) != 1 || p["databases"] == nil {
					return fmt.Errorf("unresolved adjacent settings were sent or cleared: %v", last)
				}
				return nil
			})(s)
		}},
	}})
}
