package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func TestAccDatabasePostgresLifecycle(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	config := func(postgres string) string {
		return strings.Replace(testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}), `name = "orders-db"`, `name = "orders-db"`+"\n"+postgres, 1)
	}
	checkPatch := func(want string) resource.TestCheckFunc {
		return testAccCheckDatabaseRequests(mock, func(reqs []mockRequest) error {
			var last map[string]any
			for _, r := range reqs {
				if r.method == "PATCH" {
					last = r.body
				}
			}
			encoded, err := json.Marshal(last)
			if err != nil {
				return err
			}
			if string(encoded) != want {
				return fmt.Errorf("patch=%s want=%s", encoded, want)
			}
			return nil
		})
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), CheckDestroy: testAccCheckNoDatabases(mock), Steps: []resource.TestStep{
		{Config: config(`postgres = { roles = [{ name = "reporting" }], extensions = [{ name = "pgcrypto" }] }`), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database.test", "postgres.roles.#", "1"), resource.TestCheckResourceAttr("kupe_database.test", "postgres.extensions.#", "1"))},
		{ResourceName: "kupe_database.test", ImportStateId: "orders-db", ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name"},
		{Config: config(`postgres = { roles = [] }`), Check: resource.ComposeAggregateTestCheckFunc(checkPatch(`{"postgres":{"roles":[]}}`), resource.TestCheckResourceAttr("kupe_database.test", "postgres.extensions.#", "1"))},
		{Config: config("") + `data "kupe_database" "test" { name = kupe_database.test.name }`, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("data.kupe_database.test", "postgres.extensions.#", "1")), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kupe_database.test", plancheck.ResourceActionNoop)}}},
	}})
}

func TestAccDatabaseBindingDedicatedRotation(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	config := func(names []string, extra string) string {
		return testAccDatabaseBindingConfig(mock.url(), "prod", names, "readwrite", extra)
	}
	const addr = "kupe_database_binding.test"
	inPlace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), CheckDestroy: testAccCheckNoDatabases(mock), Steps: []resource.TestStep{
		{Config: config(ns("api"), ""), Check: resource.TestCheckResourceAttr(addr, "credentials.dedicated", "false")},
		{Config: config(ns("api", "worker"), `credentials = { dedicated = true }`), ConfigPlanChecks: inPlace, Check: testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
			if len(reqs) != 2 {
				return fmt.Errorf("requests=%v", reqs)
			}
			p := reqs[1]
			if p.method != "PATCH" || p.ifMatch == "" || len(p.body) != 2 || p.body["credentials"] == nil || p.body["namespaces"] == nil {
				return fmt.Errorf("combined PATCH=%v", p)
			}
			return nil
		})},
		{Config: config(ns("api", "worker"), `credentials = { dedicated = true, rotation_trigger = "rotation-one" }`), ConfigPlanChecks: inPlace, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttrSet(addr, "observed_rotation"), resource.TestCheckResourceAttrSet(addr, "namespace_status.0.delivered_rotation"), testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
			if len(reqs) != 3 || reqs[2].method != "POST" || reqs[2].body["rotate"] != true {
				return fmt.Errorf("rotation requests=%v", reqs)
			}
			return nil
		}))},
		{Config: config(ns("api", "worker"), `credentials = { dedicated = true }`), Check: testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
			if len(reqs) != 3 {
				return fmt.Errorf("trigger removal sent request: %v", reqs)
			}
			return nil
		})},
		{Config: config(ns("api", "worker"), `credentials = { dedicated = false }`), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)}}},
	}})
}

func TestAccDatabaseBindingCredentialsOnly(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	config := func(extra string) string {
		return testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readonly", extra)
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), CheckDestroy: testAccCheckNoDatabases(mock), Steps: []resource.TestStep{
		{Config: config("")},
		{Config: config(`credentials = { dedicated = true }`), Check: testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
			if len(reqs) != 2 || len(reqs[1].body) != 1 || reqs[1].body["credentials"] == nil {
				return fmt.Errorf("credentials-only payload=%v", reqs)
			}
			return nil
		})},
	}})
}

func TestAccDatabaseBindingCustomDefaults(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), CheckDestroy: testAccCheckNoDatabases(mock), Steps: []resource.TestStep{
		{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "custom", `role_name = "sales_reports"
credentials = { dedicated = true }`), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database_binding.test", "name", "orders-db-prod-sales-reports"), resource.TestCheckResourceAttr("kupe_database_binding.test", "secret_name", "orders-db-sales-reports"))},
		{ResourceName: "kupe_database_binding.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name", ImportStateId: "orders-db/orders-db-prod-sales-reports"},
		{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "custom", `role_name = "sales_analytics"
credentials = { dedicated = true }`), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kupe_database_binding.test", plancheck.ResourceActionDestroyBeforeCreate)}}, Check: resource.TestCheckResourceAttr("kupe_database_binding.test", "role_name", "sales_analytics")},
	}})
}

func TestAccDatabaseAccessPlanRefusals(t *testing.T) {
	for _, tt := range []struct{ name, role, extra, want string }{
		{"owner", "owner", `credentials = { dedicated = true }`, `owner bindings use shared credentials`},
		{"custom shared", "custom", `role_name = "reporting"`, `custom bindings require credentials.dedicated`},
		{"custom missing name", "custom", `credentials = { dedicated = true }`, `custom bindings require roleName`},
		{"builtin role name", "readonly", `role_name = "reporting"`, `roleName is only allowed`},
		{"shared rotation", "readwrite", `credentials = { rotation_trigger = "x" }`, `rotation_trigger requires`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), tt.role, tt.extra), PlanOnly: true, ExpectError: regexp.MustCompile(tt.want)}}})
		})
	}
}

func TestDatabaseBindingDeliveryRequiresCurrentGeneration(t *testing.T) {
	b := &client.DatabaseBinding{Generation: 2, Namespaces: []string{"api"}, Status: &client.DatabaseBindingStatus{Phase: "Ready", ObservedGeneration: 1, Namespaces: []client.DatabaseBindingNamespaceStatus{{Name: "api", Phase: "Ready"}}}}
	if databaseBindingDelivered(b) {
		t.Fatal("old credentials counted as delivered")
	}
	b.Status.ObservedGeneration = 2
	if !databaseBindingDelivered(b) {
		t.Fatal("current delivery rejected")
	}
	b.Status.Conditions = []client.Condition{{Type: "Conflict", Status: "True"}}
	if databaseBindingDelivered(b) {
		t.Fatal("conflicted delivery counted as ready")
	}
}

func TestPostgresPatchDistinguishesEmptyAndAbsent(t *testing.T) {
	prior := postgresFromAPI(&client.DatabasePostgres{Roles: &[]client.DeclaredRole{{Name: "reporting"}}, Extensions: &[]client.Extension{{Name: "pgcrypto"}}})
	plan := types.ObjectValueMust(databasePostgresAttrTypes, map[string]attr.Value{"roles": types.SetValueMust(postgresNameType, []attr.Value{}), "extensions": types.SetUnknown(postgresNameType)})
	patch := postgresChanges(plan, prior)
	if patch == nil || patch.Roles == nil || len(*patch.Roles) != 0 || patch.Extensions != nil {
		t.Fatalf("clear patch=%+v", patch)
	}
	if postgresChanges(types.ObjectNull(databasePostgresAttrTypes), prior) != nil || postgresChanges(types.ObjectUnknown(databasePostgresAttrTypes), prior) != nil {
		t.Fatal("omitted postgres changed API state")
	}
}

func testAccCheckDatabaseRequests(mock *mockKupeAPI, check func([]mockRequest) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return check(mock.databaseRequests)
	}
}

func TestAccDatabaseBindingRotationFollowsLatestDelivery(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	token := "newer-opaque-token"
	status := func(applied, delivered string) map[string]any {
		return map[string]any{
			"rotation": map[string]any{"requestedAt": token, "appliedAt": applied, "pending": applied != token}, "observedRotation": applied,
			"namespaces": []any{map[string]any{"name": "api", "phase": "Ready", "host": "orders-db.api.svc.cluster.local", "message": "", "deliveredRotation": delivered}},
		}
	}
	config := func(trigger string) string {
		return testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", `credentials = { dedicated = true`+trigger+` }`)
	}
	var before int
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), CheckDestroy: testAccCheckNoDatabases(mock), Steps: []resource.TestStep{
		{Config: config("")},
		{PreConfig: func() {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			before = mock.databaseBindingGets
			mock.databaseBindingRotationStatuses = []map[string]any{status("old", "old"), status(token, "old"), status(token, token)}
		}, Config: config(`, rotation_trigger = "change"`), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("kupe_database_binding.test", "observed_rotation", token), resource.TestCheckResourceAttr("kupe_database_binding.test", "namespace_status.0.delivered_rotation", token), testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
			if len(reqs) != 2 || mock.databaseBindingGets-before < 3 {
				return fmt.Errorf("rotation did not wait for delivery: requests=%v gets=%d", reqs, mock.databaseBindingGets-before)
			}
			return nil
		}))},
	}})
}

func TestDatabaseBindingFailureDoesNotWaitForDeliveredGeneration(t *testing.T) {
	b := &client.DatabaseBinding{Generation: 2, Namespaces: []string{"api"}, Status: &client.DatabaseBindingStatus{ObservedGeneration: 1, Phase: "Failed", Conditions: []client.Condition{{Type: "Conflict", Status: "True"}}, Namespaces: []client.DatabaseBindingNamespaceStatus{{Name: "api", Phase: "Failed"}}}}
	if !databaseBindingFailed(b) {
		t.Fatal("current namespace failure hidden behind delivery generation")
	}
	b.Status.Namespaces[0].Phase = "Pending"
	if !databaseBindingFailed(b) {
		t.Fatal("current conflict hidden behind delivery generation")
	}
	b.Status.Namespaces[0].Name = "removed"
	if databaseBindingFailed(b) {
		t.Fatal("removed namespace failure treated as current")
	}
}

func TestAccDatabaseBindingRotationErrors(t *testing.T) {
	for _, test := range []string{"failed namespace", "deadline", "rejected request retries on next apply"} {
		t.Run(test, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			shrinkPollInterval(t)
			config := func(trigger string) string {
				return testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", `credentials = { dedicated = true`+trigger+` }
timeouts = { update = "100ms" }`)
			}
			steps := []resource.TestStep{{Config: config("")}, {Config: config(`, rotation_trigger = "new"`), PreConfig: func() {
				mock.mu.Lock()
				defer mock.mu.Unlock()
				if test == "rejected request retries on next apply" {
					mock.databaseBindingRotateRefusal = &mockRefusal{status: 409, code: "terminating", message: "binding is being deleted"}
					return
				}
				status := map[string]any{"rotation": map[string]any{"requestedAt": "pending-opaque", "appliedAt": "old", "pending": true}, "observedRotation": "old"}
				if test == "failed namespace" {
					status["phase"] = "Failed"
					status["observedGeneration"] = 0
					status["namespaces"] = []any{map[string]any{"name": "api", "phase": "Failed", "message": "test conflict", "deliveredRotation": "old"}}
				}
				mock.databaseBindingRotationStatuses = []map[string]any{status}
			}, ExpectError: regexp.MustCompile(`failed to (wait for database binding rotation|rotate database binding credentials)`)}}
			if test == "rejected request retries on next apply" {
				steps = append(steps, resource.TestStep{Config: config(`, rotation_trigger = "new"`), Check: testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
					if len(reqs) != 3 {
						return fmt.Errorf("rotation request not retried after rejection: %v", reqs)
					}
					return nil
				})})
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: steps})
		})
	}
}

func TestAccDatabasePostgresRoleRefusals(t *testing.T) {
	for _, name := range []string{"pg_reader", "cnpg_reader", "mdbb_reader", "mdba_reader", "mdbl_reader", "app_owner", "reporting_readonly", "Sales", "1sales", "bad-name"} {
		t.Run(name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			config := strings.Replace(testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}), `name = "orders-db"`, `name = "orders-db"`+fmt.Sprintf("\n postgres = {roles=[{name=%q}]}", name), 1)
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: config, PlanOnly: true, ExpectError: regexp.MustCompile(`Reserved role name|Invalid Attribute Value Match`)}}})
			if len(mock.databaseRequests) > 0 {
				t.Fatal("invalid role reached API")
			}
		})
	}
}

func TestDatabaseBindingRotationRequiresEveryDeliveryToken(t *testing.T) {
	b := &client.DatabaseBinding{Generation: 2, Namespaces: []string{"api"}, Status: &client.DatabaseBindingStatus{ObservedGeneration: 2, Phase: "Ready", ObservedRotation: "opaque-new", Rotation: &client.BindingRotation{RequestedAt: "opaque-new", AppliedAt: "opaque-new"}, Namespaces: []client.DatabaseBindingNamespaceStatus{{Name: "api", Phase: "Ready", DeliveredRotation: "opaque-old"}}}}
	if databaseBindingRotationDelivered(b, "opaque-new") {
		t.Fatal("rotation completed before namespace delivery")
	}
	b.Status.Namespaces[0].DeliveredRotation = "opaque-new"
	if !databaseBindingRotationDelivered(b, "opaque-new") {
		t.Fatal("completed rotation refused")
	}
	b.Status.Rotation.Pending = true
	if databaseBindingRotationDelivered(b, "opaque-new") {
		t.Fatal("pending rotation completed")
	}
	b.Status.Rotation.Pending = false
	b.Status.ObservedGeneration = 1
	if databaseBindingRotationDelivered(b, "opaque-new") {
		t.Fatal("rotation completed before credential generation delivered")
	}
	b.Status.ObservedGeneration = 2
	if databaseBindingRotationDelivered(b, "another-token") {
		t.Fatal("unrelated token completed")
	}
	b.Status.Rotation.RequestedAt = ""
	if databaseBindingRotationDelivered(b, "opaque-new") {
		t.Fatal("empty request token completed")
	}
}

func TestAccDatabaseExtensionsInvalidName(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	config := strings.Replace(testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)}), `name = "orders-db"`, `name = "orders-db"`+`
 postgres = {extensions=[{name="PGCRYPTO"}]}`, 1)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{Config: config, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid Attribute Value Match`)}}})
	if len(mock.databaseRequests) > 0 {
		t.Fatal("invalid extension reached API")
	}
}

func TestAccDatabaseExtensionChangeWaitShowsReadyReason(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	config := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false), updateTimeout: "50ms"})
	changed := strings.Replace(config, `name = "orders-db"`, `name = "orders-db"`+`
 postgres = {extensions=[{name="pgcrypto"}]}`, 1)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: config},
		{PreConfig: func() {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			mock.databaseStatusesOnPatch = []map[string]any{{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "ExtensionsPending", "message": "ExtensionsReady: waiting for pgcrypto"}}}}
		}, Config: changed, ExpectError: regexp.MustCompile(wrappedText("ExtensionsReady: waiting for pgcrypto"))},
	}})
}

func TestDatabaseTimeoutIncludesReadyReason(t *testing.T) {
	db := &client.Database{Status: &client.DatabaseStatus{Conditions: []client.Condition{{Type: client.ConditionReady, Status: "False", Message: "ExtensionsReady: waiting for pgcrypto"}}}}
	detail := databaseKind.ready.timedOutDetail(&DatabaseResourceModel{}, db)
	if !strings.Contains(detail, "ExtensionsReady: waiting for pgcrypto") {
		t.Fatalf("extension reason absent from timeout: %q", detail)
	}
	if got := databaseKind.ready.timedOutDetail(&DatabaseResourceModel{}, nil); got != "" {
		t.Fatalf("nil status detail=%q", got)
	}
}
