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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func logicalConfig(mock *mockKupeAPI, databases string) string {
	return strings.Replace(testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}), `name = "orders-db"`, `name = "orders-db"`+"\n postgres = { roles = [{name=\"reporting\"}], extensions=[{name=\"pgcrypto\"}], databases = "+databases+" }", 1)
}

const logicalTwo = `[{name="orders_db"},{name="analytics",schemas=[{name="reporting"}]}]`
const logicalThree = `[{name="orders_db"},{name="analytics",schemas=[{name="reporting"}]},{name="warehouse"}]`

func logicalAction(action plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kupe_database.test", action)}}
}
func TestAccDatabaseResource_LogicalDatabases(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), CheckDestroy: testAccCheckNoDatabases(mock), Steps: []resource.TestStep{
		{Config: logicalConfig(mock, logicalTwo), Check: resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.1.schemas.0.name", "reporting")},
		{Config: logicalConfig(mock, logicalThree), ConfigPlanChecks: logicalAction(plancheck.ResourceActionUpdate), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.#", "3"), testAccCheckDatabaseRequests(mock, func(reqs []mockRequest) error {
			last := reqs[len(reqs)-1]
			p, _ := last.body["postgres"].(map[string]any)
			if last.method != "PATCH" || len(p) != 1 || p["databases"] == nil {
				return fmt.Errorf("append must PATCH only databases, got %v", last)
			}
			return nil
		}))},
		{Config: logicalConfig(mock, `[{name="orders_db"},{name="warehouse"},{name="analytics",schemas=[{name="reporting"},{name="events"}]}]`), ConfigPlanChecks: logicalAction(plancheck.ResourceActionUpdate)},
		{Config: logicalConfig(mock, `[{name="orders_db"},{name="warehouse"}]`), PlanOnly: true, ExpectError: regexp.MustCompile("cannot be removed")},
		{Config: logicalConfig(mock, `[{name="orders_db"},{name="warehouse"},{name="analytics",schemas=[{name="events"}]}]`), PlanOnly: true, ExpectError: regexp.MustCompile("cannot be removed")},
		{Config: logicalConfig(mock, `[{name="warehouse"},{name="orders_db"},{name="analytics",schemas=[{name="reporting"},{name="events"}]}]`), PlanOnly: true, ExpectError: regexp.MustCompile("first entry must be")},
		{Config: logicalConfig(mock, `[{name="orders_db"},{name="warehouse"},{name="renamed",schemas=[{name="reporting"},{name="events"}]}]`), PlanOnly: true, ExpectError: regexp.MustCompile("cannot be removed")},
		{Config: logicalConfig(mock, `[]`), PlanOnly: true, ExpectError: regexp.MustCompile("at least 1")},
		{Config: logicalConfig(mock, `[{name="orders_db"},{name="warehouse"},{name="analytics",schemas=[{name="reporting"},{name="events"}]}]`)},
	}})
}
func TestAccDatabaseResource_LogicalOmissionAndNull(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	omitted := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: logicalConfig(mock, logicalTwo)},
		{Config: logicalConfig(mock, "null"), ConfigPlanChecks: logicalAction(plancheck.ResourceActionNoop)},
		{Config: omitted, ConfigPlanChecks: logicalAction(plancheck.ResourceActionNoop)},
		{Config: omitted + `data "kupe_database" "logical" {name=kupe_database.test.name}`, Check: resource.TestCheckResourceAttr("data.kupe_database.logical", "postgres.databases.1.name", "analytics")},
	}})
}
func TestAccDatabaseResource_LogicalValidation(t *testing.T) {
	for _, tt := range []struct{ name, list, message string }{
		{"wrong first", `[{name="wrong"}]`, "first entry must be"},
		{"long", `[{name="orders_db"},{name="` + strings.Repeat("a", 54) + `"}]`, "53"},
		{"reserved", `[{name="orders_db"},{name="samegroup"}]`, "Reserved database name"},
		{"bare prefix", `[{name="orders_db"},{name="mdbl"}]`, "Reserved database name"},
		{"app", `[{name="orders_db"},{name="app"}]`, "Reserved database name"},
		{"duplicate", `[{name="orders_db"},{name="orders_db"}]`, "Duplicate database name"},
		{"schema reserved", `[{name="orders_db",schemas=[{name="pg_private"}]}]`, "Invalid schema name"},
		{"schema duplicate", `[{name="orders_db",schemas=[{name="events"},{name="events"}]}]`, "Invalid schema name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: logicalConfig(mock, tt.list), PlanOnly: true, ExpectError: regexp.MustCompile(tt.message)}}})
		})
	}
}
func TestLogicalDatabasePatchAndProtectionOnly(t *testing.T) {
	first := []client.LogicalDatabase{{Name: "restored_first"}, {Name: "analytics", Schemas: []client.DatabaseSchema{{Name: "reporting"}}}}
	next := append(append([]client.LogicalDatabase{}, first...), client.LogicalDatabase{Name: "warehouse"})
	prior := DatabaseResourceModel{databaseModel: databaseModel{Resources: &ResourcesModel{CPU: types.StringValue("500m"), Memory: types.StringValue("1Gi"), Storage: types.StringValue("10Gi")}, Postgres: postgresFromAPI(&client.DatabasePostgres{Databases: &first, Roles: &[]client.DeclaredRole{{Name: "reporting"}}, Extensions: &[]client.Extension{{Name: "pgcrypto"}}})}}
	plan := prior
	attrs := plan.Postgres.Attributes()
	attrs["databases"] = logicalDatabasesValue(&next)
	plan.Postgres = types.ObjectValueMust(databasePostgresAttrTypes, attrs)
	patch, changed := databasePatch(&plan, &prior)
	if !changed || patch.Postgres == nil || patch.Postgres.Databases == nil || patch.Postgres.Roles != nil || patch.Postgres.Extensions != nil {
		t.Fatalf("database patch clears unrelated settings: %#v", patch.Postgres)
	}
	protection := false
	patch.DeletionProtection = &protection
	if protectionOnly(patch) {
		t.Fatal("database append skipped convergence wait")
	}
	if !protectionOnly(client.PatchDatabaseRequest{DeletionProtection: &protection}) {
		t.Fatal("protection-only update must stay available")
	}
	got := settingsChanged(&prior.databaseModel, plan.databaseModel)
	if len(got) != 1 || got[0] != "postgres.databases" {
		t.Fatalf("changes=%v", got)
	}
	wire, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "roles") || strings.Contains(string(wire), "extensions") {
		t.Fatalf("unrelated lists on wire: %s", wire)
	}
}
func TestLogicalDatabaseModifierRestoredIdentityAndUnknown(t *testing.T) {
	ctx := context.Background()
	r := NewDatabaseResource().(*DatabaseResource)
	s := testResourceSchema(t, r)
	raw := testPlanValue(t, s, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "orders-copy")})
	old := []client.LogicalDatabase{{Name: "original_db"}, {Name: "analytics", Schemas: []client.DatabaseSchema{{Name: "reporting"}}}}
	for _, tt := range []struct {
		name string
		plan types.List
		bad  bool
	}{
		{"restored append", logicalDatabasesValue(&[]client.LogicalDatabase{{Name: "original_db"}, {Name: "analytics", Schemas: old[1].Schemas}, {Name: "warehouse"}}), false},
		{"target derived first refused", logicalDatabasesValue(&[]client.LogicalDatabase{{Name: "orders_copy"}, {Name: "analytics", Schemas: old[1].Schemas}}), true},
		{"unknown list deferred", types.ListUnknown(logicalDatabaseType), false},
		{"unknown first deferred", types.ListValueMust(logicalDatabaseType, []attr.Value{types.ObjectValueMust(logicalDatabaseType.AttrTypes, map[string]attr.Value{"name": types.StringUnknown(), "schemas": types.ListUnknown(postgresNameType)}), logicalDatabasesValue(&old).Elements()[1]}), false},
		{"too few unknown entries refused", types.ListValueMust(logicalDatabaseType, []attr.Value{types.ObjectUnknown(logicalDatabaseType.AttrTypes)}), true},
		{"removed database", logicalDatabasesValue(&[]client.LogicalDatabase{old[0]}), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := planmodifier.ListRequest{Path: path.Root("postgres").AtName("databases"), State: tfsdk.State{Schema: s.Schema, Raw: raw}, Plan: tfsdk.Plan{Schema: s.Schema, Raw: raw}, StateValue: logicalDatabasesValue(&old), PlanValue: tt.plan}
			resp := planmodifier.ListResponse{PlanValue: tt.plan}
			logicalDatabasesOnlyGrow().PlanModifyList(ctx, req, &resp)
			if resp.Diagnostics.HasError() != tt.bad {
				t.Fatalf("diagnostics=%v", resp.Diagnostics)
			}
		})
	}
}
func TestLogicalDatabaseBindingSecretFallback(t *testing.T) {
	for _, tt := range []struct{ name, logical, first, want string }{
		{"ordinary first", "orders_db", "orders_db", "orders-db-readwrite"},
		{"added", "sales_data", "orders_db", "orders-db-sales-data-readwrite"},
		{"restored explicit first", "historical_name", "historical_name", "orders-db-readwrite"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var state DatabaseBindingResourceModel
			mapDatabaseBindingToState(&client.DatabaseBinding{Database: "orders-db", LogicalDatabase: tt.logical, FirstLogicalDatabase: tt.first, Role: "readwrite"}, "etag", &state)
			if state.SecretName.ValueString() != tt.want {
				t.Fatalf("secret=%s", state.SecretName.ValueString())
			}
		})
	}
}
func TestAccDatabaseBindingResource_LogicalDatabases(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	config := func(logical string, namespaces []string) string {
		cfg := testAccDatabaseBindingConfig(mock.url(), "prod", namespaces, "readwrite", `logical_database = "`+logical+`"`)
		return strings.Replace(cfg, `name = "orders-db"`, `name = "orders-db"`+"\n postgres={databases="+logicalTwo+"}", 1)
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: config("analytics", ns("api")), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database_binding.test", "name", "orders-db-analytics-prod-readwrite"), resource.TestCheckResourceAttr("kupe_database_binding.test", "secret_name", "orders-db-analytics-readwrite"), resource.TestCheckResourceAttr("kupe_database_binding.test", "logical_database", "analytics"))},
		{ResourceName: "kupe_database_binding.test", ImportState: true, ImportStateId: "orders-db/orders-db-analytics-prod-readwrite", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name"},
		{Config: config("analytics", ns("api", "worker")), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kupe_database_binding.test", plancheck.ResourceActionUpdate)}}},
		{Config: config("orders_db", ns("api", "worker")), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kupe_database_binding.test", plancheck.ResourceActionDestroyBeforeCreate)}}},
	}})
}
func TestAccDatabaseResource_LogicalDriftRefused(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: logicalConfig(mock, logicalTwo)},
		{PreConfig: func() {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			p := mock.databases["orders-db"]["postgres"].(map[string]any)
			p["databases"] = append(p["databases"].([]any), map[string]any{"name": "external", "schemas": []any{}})
		}, Config: logicalConfig(mock, logicalTwo), PlanOnly: true, ExpectError: regexp.MustCompile("cannot be removed")},
	}})
}
func TestAccDatabaseResource_LogicalRestoreOverrideRefused(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: testAccRestoreConfig(mock.url(), `backup = kupe_database_backup.test.name`, `postgres={databases=[{name="orders_db"}]}`), PlanOnly: true, ExpectError: regexp.MustCompile("Restore database settings after creation")}}})
}

// Keep a state inspection helper for privacy checks in the real CLI matrix.
func logicalNoPasswordState(s *terraform.State) error {
	for _, m := range s.Modules {
		for name, r := range m.Resources {
			if !strings.HasPrefix(name, "kupe_database") {
				continue
			}
			for key := range r.Primary.Attributes {
				if strings.Contains(key, "password") || strings.Contains(key, "username") {
					return fmt.Errorf("credential field %q in %s state", key, name)
				}
			}
		}
	}
	return nil
}

func TestAccDatabaseResource_LogicalUnknownAppend(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: logicalConfig(mock, logicalTwo)},
		{Config: logicalConfig(mock, `[{name="orders_db"},{name="analytics",schemas=[{name="reporting"}]},{name=terraform_data.late.output}]`) + `resource "terraform_data" "late" {input="warehouse"}`, ConfigPlanChecks: logicalAction(plancheck.ResourceActionUpdate), Check: resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.2.name", "warehouse")},
	}})
}
func TestAccDatabaseResource_LogicalAppendWaits(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: logicalConfig(mock, logicalTwo)},
		{PreConfig: func() {
			mock.setState(func(m *mockKupeAPI) {
				m.databaseStatusesOnPatch = []map[string]any{mockDatabaseStatus("Running", false, "setting up warehouse"), mockDatabaseStatus("Running", false, "setting up warehouse"), mockDatabaseStatus("Running", true, "ready")}
			})
		}, Config: logicalConfig(mock, logicalThree), Check: resource.ComposeAggregateTestCheckFunc(testAccCheckDatabaseStatusesPlayed(mock), logicalNoPasswordState)},
	}})
}
func TestAccDatabaseResource_LogicalAppendFailure(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	setDegradedGrace(t, 0)
	failed := mockDatabaseStatus("Degraded", false, "warehouse setup failed")
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: logicalConfig(mock, logicalTwo)},
		{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.databaseStatusesOnPatch = []map[string]any{failed} }) }, Config: logicalConfig(mock, logicalThree), ExpectError: regexp.MustCompile("warehouse setup failed")},
		{PreConfig: func() {
			mock.setState(func(m *mockKupeAPI) { m.databaseStatusesOnPatch = nil; m.databaseStatuses = nil })
		}, Config: logicalConfig(mock, logicalThree), ConfigPlanChecks: logicalAction(plancheck.ResourceActionNoop), Check: resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.2.name", "warehouse")},
	}})
}
func TestAccDatabaseBindingResource_LogicalDefaultNameClash(t *testing.T) {
	for _, kind := range []string{"binding", "secret"} {
		t.Run(kind, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			shrinkPollInterval(t)
			existing := map[string]any{"name": "orders-db-analytics-prod-readwrite", "database": "orders-db-analytics", "cluster": "prod", "namespaces": []string{"api"}, "role": "readwrite", "logicalDatabase": "orders_db_analytics", "secretName": ""}
			if kind == "secret" {
				existing["name"] = "another-binding"
			}
			mock.databaseBindings["orders-db-analytics/occupied"] = existing
			mock.databases["orders-db-analytics"] = map[string]any{"name": "orders-db-analytics"}
			config := testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", `logical_database="analytics"`)
			config = strings.Replace(config, `name = "orders-db"`, `name = "orders-db"`+"\n postgres={databases="+logicalTwo+"}", 1)
			hint := "Set `name` explicitly"
			if kind == "secret" {
				hint = "Set `secret_name` explicitly"
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: config, ExpectError: regexp.MustCompile(hint)}}})
			if len(mock.databaseBindingRequests) != 1 {
				t.Fatalf("retried colliding name: %v", mock.databaseBindingRequests)
			}
		})
	}
}

// The fixture is an API response for an already restored instance, not a
// recovery simulation: the current effective list owns its SQL identity.
func TestAccDatabaseResource_LogicalRestoredFirstUpdate(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	initial := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})
	restored := logicalConfig(mock, `[{name="historical_db"},{name="analytics"}]`)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: initial},
		{PreConfig: func() {
			mock.setState(func(m *mockKupeAPI) {
				db := m.databases["orders-db"]
				db["firstLogicalDatabase"] = "historical_db"
				db["postgres"] = map[string]any{"databases": []any{map[string]any{"name": "historical_db", "schemas": []any{}}}}
			})
		}, Config: restored, ConfigPlanChecks: logicalAction(plancheck.ResourceActionUpdate), Check: resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.0.name", "historical_db")},
	}})
}
func TestLogicalBindingCollisionMessagesVerbatim(t *testing.T) {
	message := `secret name "orders-analytics-readwrite" is already used in namespace "api"; set spec.secretName to another name`
	got := databaseBindingCreateErrorDetail(&client.APIError{StatusCode: 400, Message: message})
	if !strings.Contains(got, message) || !strings.Contains(got, "Set `secret_name`") {
		t.Fatalf("API refusal/hint changed: %s", got)
	}
}

func TestAccDatabaseBindingResource_LogicalUnknownSelection(t *testing.T) {
	for _, tt := range []struct{ name, selection, extra, wantName string }{
		{"first", "orders_db", "", "orders-db-prod-readwrite"},
		{"added", "analytics", "", "orders-db-analytics-prod-readwrite"},
		{"explicit name", "analytics", `name="selected-binding"`, "selected-binding"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			cfg := testAccDatabaseBindingConfig(m.url(), "prod", ns("api"), "readwrite", `logical_database=terraform_data.choice.output`+"\n"+tt.extra) + `resource "terraform_data" "choice" {input="` + tt.selection + `"}`
			cfg = strings.Replace(cfg, `name = "orders-db"`, `name = "orders-db"`+"\n postgres={databases="+logicalTwo+"}", 1)
			secret := "orders-db-readwrite"
			if tt.selection == "analytics" {
				secret = "orders-db-analytics-readwrite"
			}
			checks := []plancheck.PlanCheck{plancheck.ExpectUnknownValue("kupe_database_binding.test", tfjsonpath.New("name"))}
			if tt.extra != "" {
				checks = []plancheck.PlanCheck{plancheck.ExpectKnownValue("kupe_database_binding.test", tfjsonpath.New("name"), knownvalue.StringExact(tt.wantName))}
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
				{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: checks}, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database_binding.test", "logical_database", tt.selection), resource.TestCheckResourceAttr("kupe_database_binding.test", "name", tt.wantName), resource.TestCheckResourceAttr("kupe_database_binding.test", "secret_name", secret))},
			}})
		})
	}
}
func TestAccDatabaseBindingResource_LogicalDefaultUnknownOtherPart(t *testing.T) {
	for _, part := range []string{"cluster", "role", "database"} {
		t.Run(part, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			cfg := testAccDatabaseBindingConfig(m.url(), "prod", ns("api"), "readwrite", `logical_database="analytics"`)
			cfg = strings.Replace(cfg, `name = "orders-db"`, `name = "orders-db"`+"\n postgres={databases="+logicalTwo+"}", 1)
			switch part {
			case "cluster":
				cfg = strings.Replace(cfg, `cluster    = "prod"`, `cluster = terraform_data.late.output`, 1) + `resource "terraform_data" "late" {input="prod"}`
			case "role":
				cfg = strings.Replace(cfg, `role       = "readwrite"`, `role = terraform_data.late.output`, 1) + `resource "terraform_data" "late" {input="readwrite"}`
			case "database":
				cfg = strings.Replace(cfg, `database   = kupe_database.test.name`, `database = terraform_data.late.output`, 1) + `resource "terraform_data" "late" {input=kupe_database.test.name}`
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: cfg, Check: resource.TestCheckResourceAttr("kupe_database_binding.test", "name", "orders-db-analytics-prod-readwrite")}}})
		})
	}
}

func TestAccDatabaseBindingResource_LogicalOmittedNullKnownName(t *testing.T) {
	for _, selection := range []string{"", "logical_database=null"} {
		t.Run(selection, func(t *testing.T) {
			m := newMockKupeAPI()
			defer m.close()
			shrinkPollInterval(t)
			cfg := testAccDatabaseBindingConfig(m.url(), "prod", ns("api"), "readwrite", selection)
			cfg = strings.Replace(cfg, `name = "orders-db"`, `name = "orders-db"`+"\n postgres={databases="+logicalTwo+"}", 1)
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectKnownValue("kupe_database_binding.test", tfjsonpath.New("name"), knownvalue.StringExact("orders-db-prod-readwrite"))}}, Check: resource.TestCheckResourceAttr("kupe_database_binding.test", "logical_database", "orders_db")}}})
		})
	}
}
