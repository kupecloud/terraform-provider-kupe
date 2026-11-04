package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// These are actual CLI plan/apply controls. Unknown-object negatives must get
// past the initial plan and resolve terraform_data before the provider refuses.
func TestAccDatabaseResource_LogicalObjectMandatoryNames(t *testing.T) {
	cases := []struct{ name, object string }{
		{"missing", `{}`}, {"null name", `{name=null}`}, {"empty name", `{name=""}`}, {"null entry", `null`},
		{"invalid name", `{name="Bad!"}`},
	}
	for _, kind := range []string{"database", "schema"} {
		for _, phase := range []string{"create", "update"} {
			for _, unknown := range []bool{false, true} {
				for _, tt := range cases {
					t.Run(fmt.Sprintf("%s/%s/unknown=%t/%s", kind, phase, unknown, tt.name), func(t *testing.T) {
						m := newMockKupeAPI()
						defer m.close()
						shrinkPollInterval(t)
						planSeen := false
						value := tt.object
						extra := ""
						if unknown {
							value = "terraform_data.late.output"
							input := tt.object
							if tt.name == "null entry" {
								input = `"resolved"`
								value = `terraform_data.late.output == "resolved" ? null : {name="unused"}`
							}
							extra = `resource "terraform_data" "late" {input=` + input + `}`
						}
						list := `[{name="orders_db"},` + value + `]`
						if kind == "schema" {
							list = `[{name="orders_db",schemas=[` + value + `]}]`
						}
						steps := []resource.TestStep{}
						if phase == "update" {
							steps = append(steps, resource.TestStep{Config: logicalConfig(m, `[{name="orders_db"}]`)})
						}
						rejected := resource.TestStep{Config: logicalConfig(m, list) + extra, ExpectError: regexp.MustCompile(`PostgreSQL name required|Invalid PostgreSQL entry|Invalid PostgreSQL name|Invalid Attribute Value`), PlanOnly: !unknown}
						if unknown {
							rejected.ConfigPlanChecks = resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{logicalPrototypePlanCapture{t: t, seen: &planSeen}}}
						}
						steps = append(steps, rejected)
						cleanup := testAccProviderConfig(m.url())
						if phase == "update" {
							cleanup = logicalConfig(m, `[{name="orders_db"}]`)
						}
						last := resource.TestStep{Config: cleanup + extra}
						if unknown {
							last.PreConfig = func() {
								if !planSeen {
									t.Error("unknown invalid object was refused before the saved plan/apply control")
								}
							}
							last.Check = resource.TestCheckResourceAttrSet("terraform_data.late", "id")
						}
						steps = append(steps, last)
						resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: steps})
						m.mu.Lock()
						defer m.mu.Unlock()
						want := 0
						if phase == "update" {
							want = 1
						}
						if len(m.databaseRequests) != want {
							t.Errorf("invalid name reached POST/PATCH: got %v; want %d baseline writes", m.databaseRequests, want)
						}
					})
				}
			}
		}
	}
}

func TestAccDatabaseResource_LogicalWholeObjectPositive(t *testing.T) {
	for _, tt := range []struct {
		name, initial, list, input string
		writes                     int
		restored                   bool
	}{
		{"create first", "", `[terraform_data.late.output]`, `{name="orders_db"}`, 1, false},
		{"existing database", logicalTwo, `[{name="orders_db"},terraform_data.late.output]`, `{name="analytics",schemas=[{name="reporting"}]}`, 1, false},
		{"existing database omitted schemas", logicalTwo, `[{name="orders_db"},terraform_data.late.output]`, `{name="analytics"}`, 1, false},
		{"added database", `[{name="orders_db"}]`, `[{name="orders_db"},terraform_data.late.output]`, `{name="analytics",schemas=[{name="reporting"}]}`, 2, false},
		{"existing schema", logicalTwo, `[{name="orders_db"},{name="analytics",schemas=[terraform_data.late.output]}]`, `{name="reporting"}`, 1, false},
		{"added schema", logicalTwo, `[{name="orders_db"},{name="analytics",schemas=[{name="reporting"},terraform_data.late.output]}]`, `{name="events"}`, 2, false},
		{"existing first", logicalTwo, `[terraform_data.late.output,{name="analytics",schemas=[{name="reporting"}]}]`, `{name="orders_db"}`, 1, false},
		{"restored first", logicalTwo, `[terraform_data.late.output,{name="analytics",schemas=[{name="reporting"}]}]`, `{name="historical_db"}`, 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			steps := []resource.TestStep{}
			if tt.initial != "" {
				steps = append(steps, resource.TestStep{Config: logicalConfig(m, tt.initial)})
			}
			next := resource.TestStep{Config: logicalConfig(m, tt.list) + `resource "terraform_data" "late" {input=` + tt.input + `}`}
			if tt.restored {
				next.PreConfig = func() {
					m.setState(func(m *mockKupeAPI) {
						db := m.databases["orders-db"]
						db["firstLogicalDatabase"] = "historical_db"
						db["postgres"].(map[string]any)["databases"].([]any)[0].(map[string]any)["name"] = "historical_db"
					})
				}
			}
			next.Check = testAccCheckDatabaseRequests(m, func(reqs []mockRequest) error {
				if len(reqs) != tt.writes {
					return fmt.Errorf("got %v writes, want %d", reqs, tt.writes)
				}
				for _, req := range reqs {
					if req.method != "POST" && req.method != "PATCH" {
						return fmt.Errorf("replacement: %v", req)
					}
				}
				return nil
			})
			steps = append(steps, next)
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: steps})
		})
	}
}

func TestLogicalReservedExactClientCopy(t *testing.T) {
	for _, name := range []string{"app", "pg", "cnpg", "mdba", "mdbb", "mdbl", "pg_reports", "cnpg_reports", "mdba_reports", "mdbb_reports", "mdbl_reports"} {
		want := fmt.Sprintf("%q is reserved: added databases cannot be named app, pg, cnpg, mdba, mdbb or mdbl, or start with pg_, cnpg_, mdba_, mdbb_ or mdbl_.", name)
		if got := reservedLogicalDatabaseMessage(1, name); got != want {
			t.Errorf("%s: %s", name, got)
		}
		if strings.Contains(reservedLogicalDatabaseMessage(0, name), "added database") {
			t.Errorf("first exemption lost: %s", name)
		}
		first := logicalDatabasesValue(&[]client.LogicalDatabase{{Name: name}})
		response := validator.ListResponse{}
		logicalNamesValidator{}.ValidateList(context.Background(), validator.ListRequest{Path: path.Root("postgres").AtName("databases"), ConfigValue: first}, &response)
		if response.Diagnostics.HasError() {
			t.Errorf("first name %q refused: %v", name, response.Diagnostics)
		}

	}
}

type logicalPrototypePlanCapture struct {
	t    *testing.T
	seen *bool
}

func (c logicalPrototypePlanCapture) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, _ *plancheck.CheckPlanResponse) {
	*c.seen = true
	encoded, err := json.Marshal(req.Plan)
	if err != nil {
		c.t.Error(err)
		return
	}
	c.t.Logf("ACTUAL SAVED PLAN: %s", encoded)
}

func TestAccDatabaseResource_LogicalResolvedConstraints(t *testing.T) {
	manyDB := `[{name="orders_db"}`
	for i := 0; i < 10; i++ {
		manyDB += fmt.Sprintf(`,{name="extra_%d"}`, i)
	}
	manyDB += `]`
	manySchemas := `[{name="orders_db",schemas=[`
	for i := 0; i < 21; i++ {
		if i > 0 {
			manySchemas += ","
		}
		manySchemas += fmt.Sprintf(`{name="extra_%d"}`, i)
	}
	manySchemas += `]}]`
	for _, phase := range []string{"create", "update"} {
		for _, tt := range []struct{ name, list, message string }{
			{"first", `[{name="changed"}]`, `first entry must be`},
			{"reserved", `[{name="orders_db"},{name="app"}]`, `is reserved: added databases`},
			{"duplicate", `[{name="orders_db"},{name="orders_db"}]`, `Duplicate database name`},
			{"database cap", manyDB, `at most 10`},
			{"schema cap", manySchemas, `at most 20`},
			{"schema reserved", `[{name="orders_db",schemas=[{name="pg_private"}]}]`, `Invalid schema name`},
			{"schema duplicate", `[{name="orders_db",schemas=[{name="events"},{name="events"}]}]`, `Invalid schema name`},
		} {
			t.Run(phase+"/"+tt.name, func(t *testing.T) {
				m := newMockKupeAPI()
				defer m.close()
				shrinkPollInterval(t)
				extra := `resource "terraform_data" "late" {input=` + tt.list + `}`
				seen := false
				steps := []resource.TestStep{}
				if phase == "update" {
					steps = append(steps, resource.TestStep{Config: logicalConfig(m, `[{name="orders_db"}]`)})
				}
				steps = append(steps, resource.TestStep{Config: logicalConfig(m, `terraform_data.late.output`) + extra, ExpectError: regexp.MustCompile(tt.message), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{logicalPrototypePlanCapture{t: t, seen: &seen}}}})
				clean := testAccProviderConfig(m.url())
				if phase == "update" {
					clean = logicalConfig(m, `[{name="orders_db"}]`)
				}
				steps = append(steps, resource.TestStep{Config: clean + extra, PreConfig: func() {
					if !seen {
						t.Error("invalid resolved list did not reach saved-plan apply")
					}
				}})
				resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: steps})
				m.mu.Lock()
				defer m.mu.Unlock()
				want := 0
				if phase == "update" {
					want = 1
				}
				if len(m.databaseRequests) != want {
					t.Errorf("invalid resolved list reached API: %v", m.databaseRequests)
				}
			})
		}
	}
}

func TestAccDatabaseBindingResource_LogicalResourceNameWords(t *testing.T) {
	for _, tt := range []struct {
		name, selection, explicit, want, secret string
		restored                                bool
	}{
		{"omitted", "", "", "orders-db-prod-readwrite", "orders-db-readwrite", false},
		{"normalised first", "orders_db", "", "orders-db-prod-readwrite", "orders-db-readwrite", false},
		{"added", "analytics", "", "orders-db-analytics-prod-readwrite", "orders-db-analytics-readwrite", false},
		{"restored omitted", "", "", "orders-db-prod-readwrite", "orders-db-readwrite", true},
		{"restored explicit", "historical_db", "", "orders-db-historical-db-prod-readwrite", "orders-db-readwrite", true},
		{"restored explicit name", "historical_db", "chosen-binding", "chosen-binding", "orders-db-readwrite", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			list := logicalTwo
			if tt.restored {
				list = strings.Replace(list, "orders_db", "historical_db", 1)
			}
			cfg := logicalConfig(m, list) + `resource "kupe_database_binding" "words" {
database=kupe_database.test.name
 cluster="prod"
 namespaces=["api"]
 role="readwrite"
 `
			if tt.selection != "" {
				cfg += fmt.Sprintf("logical_database=%q\n", tt.selection)
			}
			if tt.explicit != "" {
				cfg += fmt.Sprintf("name=%q\n", tt.explicit)
			}
			cfg += "}"
			next := resource.TestStep{Config: cfg, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database_binding.words", "name", tt.want), resource.TestCheckResourceAttr("kupe_database_binding.words", "secret_name", tt.secret))}
			if tt.restored {
				next.PreConfig = func() {
					m.setState(func(m *mockKupeAPI) {
						db := m.databases["orders-db"]
						db["firstLogicalDatabase"] = "historical_db"
						db["postgres"].(map[string]any)["databases"].([]any)[0].(map[string]any)["name"] = "historical_db"
					})
				}
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: logicalConfig(m, logicalTwo)}, next}})
		})
	}
}

// Invoke the actual CRUD entry points directly with final-known malformed
// values, independently of Framework config/plan validation.
func TestLogicalFinalCRUDMandatoryNameGuard(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"database", "schema"} {
		for _, phase := range []string{"create", "update"} {
			for _, bad := range []string{"missing", "empty", "null entry"} {
				t.Run(kind+"/"+phase+"/"+bad, func(t *testing.T) {
					m := newMockKupeAPI()
					defer m.close()
					r := NewDatabaseResource().(*DatabaseResource)
					r.client = client.New(m.url(), "acme", "kupe_test_key")
					schema := testResourceSchema(t, r)
					entryType := logicalDatabaseType
					if kind == "schema" {
						entryType = postgresNameType
					}
					attrs := map[string]attr.Value{"name": types.StringNull()}
					if kind == "database" {
						attrs["schemas"] = types.ListNull(postgresNameType)
					}
					if bad == "empty" {
						attrs["name"] = types.StringValue("")
					}
					entry := types.ObjectValueMust(entryType.AttrTypes, attrs)
					if bad == "null entry" {
						entry = types.ObjectNull(entryType.AttrTypes)
					}
					first := logicalDatabasesValue(&[]client.LogicalDatabase{{Name: "orders_db"}}).Elements()[0]
					list := types.ListNull(logicalDatabaseType)
					if kind == "database" {
						list = types.ListValueMust(logicalDatabaseType, []attr.Value{first, entry})
					}
					if kind == "schema" {
						list = types.ListValueMust(logicalDatabaseType, []attr.Value{types.ObjectValueMust(logicalDatabaseType.AttrTypes, map[string]attr.Value{"name": types.StringValue("orders_db"), "schemas": types.ListValueMust(postgresNameType, []attr.Value{entry})})})
					}
					postgres := postgresFromAPI(&client.DatabasePostgres{})
					pa := postgres.Attributes()
					pa["databases"] = list
					postgres = types.ObjectValueMust(databasePostgresAttrTypes, pa)
					pv, err := postgres.ToTerraformValue(ctx)
					if err != nil {
						t.Fatal(err)
					}
					raw := testPlanValue(t, schema, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "orders-db"), "postgres": pv})
					plan := tfsdk.Plan{Schema: schema.Schema, Raw: raw}
					config := tfsdk.Config{Schema: schema.Schema, Raw: raw}
					if phase == "create" {
						resp := fwresource.CreateResponse{}
						r.Create(ctx, fwresource.CreateRequest{Plan: plan, Config: config}, &resp)
						if !resp.Diagnostics.HasError() {
							t.Fatal("final Create accepted malformed entry")
						}
						t.Logf("FINAL CREATE REFUSAL: %v", resp.Diagnostics)
					} else {
						old := postgresFromAPI(&client.DatabasePostgres{Databases: &[]client.LogicalDatabase{{Name: "orders_db"}}})
						ov, err := old.ToTerraformValue(ctx)
						if err != nil {
							t.Fatal(err)
						}
						prior := testPlanValue(t, schema, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "orders-db"), "postgres": ov})
						resp := fwresource.UpdateResponse{}
						r.Update(ctx, fwresource.UpdateRequest{Plan: plan, Config: config, State: tfsdk.State{Schema: schema.Schema, Raw: prior}}, &resp)
						if !resp.Diagnostics.HasError() {
							t.Fatal("final Update accepted malformed entry")
						}
						t.Logf("FINAL UPDATE REFUSAL: %v", resp.Diagnostics)
					}
					m.mu.Lock()
					defer m.mu.Unlock()
					if len(m.databaseRequests) != 0 {
						t.Errorf("final malformed entry reached API: %v", m.databaseRequests)
					}
				})
			}
		}
	}
}

func TestLogicalFinalUnresolvedValues(t *testing.T) {
	ctx := context.Background()
	for _, shape := range []string{"schemas", "databases", "postgres", "omitted postgres", "omitted databases", "known empty schemas"} {
		t.Run(shape, func(t *testing.T) {
			r := NewDatabaseResource().(*DatabaseResource)
			schema := testResourceSchema(t, r)
			first := types.ObjectValueMust(logicalDatabaseType.AttrTypes, map[string]attr.Value{"name": types.StringValue("orders_db"), "schemas": types.ListUnknown(postgresNameType)})
			pg := postgresFromAPI(&client.DatabasePostgres{})
			attrs := pg.Attributes()
			attrs["databases"] = types.ListValueMust(logicalDatabaseType, []attr.Value{first})
			if shape == "databases" || shape == "omitted databases" {
				attrs["databases"] = types.ListUnknown(logicalDatabaseType)
			}
			if shape == "known empty schemas" {
				attrs["databases"] = logicalDatabasesValue(&[]client.LogicalDatabase{{Name: "orders_db"}})
			}
			pg = types.ObjectValueMust(databasePostgresAttrTypes, attrs)
			if shape == "postgres" || shape == "omitted postgres" {
				pg = types.ObjectUnknown(databasePostgresAttrTypes)
			}
			pv, err := pg.ToTerraformValue(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rootType := schema.Schema.Type().TerraformType(ctx).(tftypes.Object)
			raw := testPlanValue(t, schema, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "orders-db"), "postgres": pv, "source": tftypes.NewValue(rootType.AttributeTypes["source"], nil)})
			configRaw := raw
			if shape == "omitted postgres" {
				configRaw = testPlanValue(t, schema, map[string]tftypes.Value{"postgres": tftypes.NewValue(rootType.AttributeTypes["postgres"], nil)})
			}
			if shape == "omitted databases" {
				cfgattrs := pg.Attributes()
				cfgattrs["databases"] = types.ListNull(logicalDatabaseType)
				cfgpg := types.ObjectValueMust(databasePostgresAttrTypes, cfgattrs)
				cv, e := cfgpg.ToTerraformValue(ctx)
				if e != nil {
					t.Fatal(e)
				}
				configRaw = testPlanValue(t, schema, map[string]tftypes.Value{"postgres": cv})
			}
			config := tfsdk.Config{Schema: schema.Schema, Raw: configRaw}
			plan := tfsdk.Plan{Schema: schema.Schema, Raw: raw}
			var model DatabaseResourceModel
			if d := plan.Get(ctx, &model); d.HasError() {
				t.Fatal(d)
			}
			diagnostics := validateLogicalWrite(ctx, config, model, DatabaseResourceModel{}, true)
			allowed := strings.HasPrefix(shape, "omitted ") || shape == "known empty schemas"
			if diagnostics.HasError() == allowed {
				t.Fatalf("final guard shape %q allowed=%t diagnostics=%v", shape, allowed, diagnostics)
			}
			if allowed {
				return
			}
			m := newMockKupeAPI()
			defer m.close()
			r.client = client.New(m.url(), "acme", "kupe_test_key")
			create := fwresource.CreateResponse{}
			r.Create(ctx, fwresource.CreateRequest{Config: config, Plan: plan}, &create)
			if !create.Diagnostics.HasError() {
				t.Fatal("final Create accepted unresolved logical values")
			}
			priorPG := postgresFromAPI(&client.DatabasePostgres{Databases: &[]client.LogicalDatabase{{Name: "orders_db"}}})
			priorValue, err := priorPG.ToTerraformValue(ctx)
			if err != nil {
				t.Fatal(err)
			}
			priorRaw := testPlanValue(t, schema, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "orders-db"), "postgres": priorValue})
			update := fwresource.UpdateResponse{}
			r.Update(ctx, fwresource.UpdateRequest{Config: config, Plan: plan, State: tfsdk.State{Schema: schema.Schema, Raw: priorRaw}}, &update)
			if !update.Diagnostics.HasError() {
				t.Fatal("final Update accepted unresolved logical values")
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if len(m.databaseRequests) != 0 {
				t.Errorf("unresolved values reached API: %v", m.databaseRequests)
			}
		})
	}
}
