package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func TestLogicalUnknownParentRetention(t *testing.T) {
	schemas := func(names ...string) types.List {
		values := make([]attr.Value, 0, len(names))
		for _, name := range names {
			values = append(values, types.ObjectValueMust(postgresNameType.AttrTypes, map[string]attr.Value{"name": types.StringValue(name)}))
		}
		return types.ListValueMust(postgresNameType, values)
	}
	db := func(name types.String, ss types.List) attr.Value {
		return types.ObjectValueMust(logicalDatabaseType.AttrTypes, map[string]attr.Value{"name": name, "schemas": ss})
	}
	first := db(types.StringValue("orders_db"), schemas())
	unknown := types.StringUnknown()
	retained := []client.LogicalDatabase{{Name: "orders_db"}, {Name: "analytics", Schemas: []client.DatabaseSchema{{Name: "reporting"}}}}
	three := append(append([]client.LogicalDatabase{}, retained...), client.LogicalDatabase{Name: "warehouse", Schemas: []client.DatabaseSchema{{Name: "reporting"}, {Name: "events"}}})
	for _, tt := range []struct {
		name   string
		old    []client.LogicalDatabase
		values []attr.Value
		refuse bool
	}{
		{"known empty cannot retain reporting", retained, []attr.Value{first, db(unknown, schemas())}, true},
		{"known different schema cannot retain reporting", retained, []attr.Value{first, db(unknown, schemas("events"))}, true},
		{"preserve reporting", retained, []attr.Value{first, db(unknown, schemas("reporting"))}, false},
		{"grow schemas", retained, []attr.Value{first, db(unknown, schemas("reporting", "events"))}, false},
		{"omitted schema list", retained, []attr.Value{first, db(unknown, types.ListNull(postgresNameType))}, false},
		{"unknown schema list", retained, []attr.Value{first, db(unknown, types.ListUnknown(postgresNameType))}, false},
		{"whole object", retained, []attr.Value{first, types.ObjectUnknown(logicalDatabaseType.AttrTypes)}, false},
		{"unknown schema name has capacity", retained, []attr.Value{first, db(unknown, types.ListValueMust(postgresNameType, []attr.Value{types.ObjectValueMust(postgresNameType.AttrTypes, map[string]attr.Value{"name": unknown})}))}, false},
		{"additional empty database while old retained", retained, []attr.Value{first, db(types.StringValue("analytics"), schemas("reporting")), db(unknown, schemas())}, false},
		{"overlap needs reassignment", three, []attr.Value{first, db(unknown, schemas("reporting", "events")), db(unknown, schemas("reporting"))}, false},
		{"two slots only one compatible", three, []attr.Value{first, db(unknown, schemas("reporting", "events")), db(unknown, schemas())}, true},
		{"first slot cannot retain added database", retained, []attr.Value{db(unknown, schemas("reporting")), db(unknown, schemas())}, true},
		{"first and added have compatible slots", retained, []attr.Value{db(unknown, schemas()), db(unknown, schemas("reporting"))}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			list := types.ListValueMust(logicalDatabaseType, tt.values)
			got := validateLogicalTransition(list, logicalDatabasesValue(&tt.old), "orders_db", false, path.Root("postgres").AtName("databases"))
			if got.HasError() != tt.refuse {
				t.Fatalf("refuse=%t, diagnostics=%v", tt.refuse, got)
			}
		})
	}
	if got := validateLogicalTransition(types.ListUnknown(logicalDatabaseType), logicalDatabasesValue(&retained), "orders_db", false, path.Root("postgres").AtName("databases")); got.HasError() {
		t.Fatal(got)
	}
}

type logicalJointPlanCheck struct {
	t    *testing.T
	seen *bool
}

func (c logicalJointPlanCheck) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	b, err := json.Marshal(req.Plan)
	if err != nil {
		resp.Error = err
		return
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		resp.Error = err
		return
	}
	for _, value := range p["resource_changes"].([]any) {
		r := value.(map[string]any)
		if r["address"] != "kupe_database.test" {
			continue
		}
		ch := r["change"].(map[string]any)
		a, _ := json.Marshal(ch["actions"])
		if string(a) != `["update"]` && string(a) != `["no-op"]` {
			resp.Error = fmt.Errorf("unexpected replacement actions: %s", a)
		}
		u := ch["after_unknown"].(map[string]any)
		if u["name"] == true {
			resp.Error = fmt.Errorf("metadata name must remain known")
		}
		*c.seen = true
	}
	c.t.Logf("JOINT_UNKNOWN_INITIAL_PLAN %s", b)
}

// The DELETE/UID oracle also distinguishes candidate refusal from fixture
// cleanup; these tests assert API state before that cleanup begins.
func TestAccDatabaseResource_LogicalJointUnknownParentSchemas(t *testing.T) {
	const overlapping = `[{name="orders_db"},{name="analytics",schemas=[{name="reporting"}]},{name="warehouse",schemas=[{name="reporting"},{name="events"}]}]`
	for _, tt := range []struct {
		name, initial, list, input string
		refuse, planOnly           bool
	}{
		{"impossible initial empty schemas", logicalTwo, `[{name="orders_db"},{name=terraform_data.late.output,schemas=[]}]`, `"analytics"`, true, true},
		{"preserve reporting", logicalTwo, `[{name="orders_db"},{name=terraform_data.late.output,schemas=[{name="reporting"}]}]`, `"analytics"`, false, false},
		{"grow schemas", logicalTwo, `[{name="orders_db"},{name=terraform_data.late.output,schemas=[{name="reporting"},{name="events"}]}]`, `"analytics"`, false, false},
		{"omit schemas", logicalTwo, `[{name="orders_db"},{name=terraform_data.late.output}]`, `"analytics"`, false, false},
		{"whole object", logicalTwo, `[{name="orders_db"},terraform_data.late.output]`, `{name="analytics",schemas=[{name="reporting"}]}`, false, false},
		{"whole list", logicalTwo, `terraform_data.late.output`, logicalTwo, false, false},
		{"new unknown empty database", logicalTwo, `[{name="orders_db"},{name="analytics",schemas=[{name="reporting"}]},{name=terraform_data.late.output,schemas=[]}]`, `"warehouse"`, false, false},
		{"overlap requires alternative assignment", overlapping, `[{name="orders_db"},{name=terraform_data.late.output.broad,schemas=[{name="reporting"},{name="events"}]},{name=terraform_data.late.output.narrow,schemas=[{name="reporting"}]}]`, `{broad="warehouse",narrow="analytics"}`, false, false},
		{"two slots only one compatible", overlapping, `[{name="orders_db"},{name=terraform_data.late.output.broad,schemas=[{name="reporting"},{name="events"}]},{name=terraform_data.late.output.narrow,schemas=[]}]`, `{broad="warehouse",narrow="analytics"}`, true, true},
		{"unresolved list final removal refused", logicalTwo, `[{name="orders_db"},{name="analytics",schemas=terraform_data.late.output}]`, `[]`, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := &replacementOracle{phase: "initial", uids: map[string]int{}}
			m := newMockKupeAPI(o.decorate)
			defer m.close()
			shrinkPollInterval(t)
			seen := false
			extra := `resource "terraform_data" "late" {input=` + tt.input + `}`
			next := resource.TestStep{PreConfig: func() { o.phaseTo("candidate") }, Config: logicalConfig(m, tt.list) + extra, PlanOnly: tt.planOnly}
			if tt.refuse {
				next.ExpectError = regexp.MustCompile("cannot be removed")
			}
			if !tt.planOnly {
				next.ConfigPlanChecks = resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{logicalJointPlanCheck{t, &seen}}}
			}
			verify := func() {
				o.snapshot(t, m, "before explicit cleanup")
				d, p, n := o.counts("candidate")
				if d != 0 || n != 0 || (tt.refuse && p != 0) {
					t.Errorf("candidate writes DELETE=%d PATCH=%d POST=%d", d, p, n)
				}
				o.mu.Lock()
				uid := o.uids["orders-db"]
				o.mu.Unlock()
				if uid != 1 {
					t.Errorf("original fixture UID lost: %d", uid)
				}
				if !tt.planOnly && !seen {
					t.Error("genuine initial unknown plan not observed")
				}
				m.mu.Lock()
				snapshot, _ := json.Marshal(m.databases["orders-db"])
				m.mu.Unlock()
				var object struct {
					Postgres struct {
						Databases []client.LogicalDatabase `json:"databases"`
					} `json:"postgres"`
				}
				if err := json.Unmarshal(snapshot, &object); err != nil {
					t.Error(err)
				}
				retained := false
				for _, database := range object.Postgres.Databases {
					if database.Name != "analytics" {
						continue
					}
					for _, schema := range database.Schemas {
						retained = retained || schema.Name == "reporting"
					}
				}
				if !retained {
					t.Errorf("analytics.reporting schema lost: %s", snapshot)
				}
			}
			steps := []resource.TestStep{{Config: logicalConfig(m, tt.initial)}, next}
			if tt.refuse {
				steps = append(steps, resource.TestStep{PreConfig: func() { verify(); o.phaseTo("explicit cleanup") }, Config: logicalConfig(m, tt.initial) + extra})
			} else {
				steps[1].Check = func(_ *terraform.State) error { verify(); o.phaseTo("teardown"); return nil }
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: steps})
		})
	}
}
