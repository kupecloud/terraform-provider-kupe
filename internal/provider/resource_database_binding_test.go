package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
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
		{name: "kube-* namespace", cluster: "prod", namespace: "kube-system", role: "app", wantErr: `Reserved namespace`},
		{name: "argocd namespace", cluster: "prod", namespace: "argocd", role: "app", wantErr: `Reserved namespace`},
		{name: "namespace with uppercase", cluster: "prod", namespace: "API", role: "app", wantErr: `must be lowercase letters`},
		{name: "cluster with a double hyphen", cluster: "prod--eu", namespace: "api", role: "app", wantErr: `not contain '--'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
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

// TestAccDatabaseBindingResource_DatabaseNameValidated: a literal database
// name the platform would refuse fails validation.
func TestAccDatabaseBindingResource_DatabaseNameValidated(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(mock.url()) + `
resource "kupe_database_binding" "test" {
  database  = "orders--db"
  cluster   = "prod"
  namespace = "api"
  role      = "app"
}
`,
				ExpectError: regexp.MustCompile(`not contain '--'`),
			},
		},
	})
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

// testAccBindingConflictMessage is the message the platform sets on a binding
// whose Service or Secret name a tenant object already holds.
const testAccBindingConflictMessage = "the namespace already has a Service or Secret of the binding's name that " +
	"the platform did not create; remove it, or bind with another secretName"

// testAccBindingConflictStatus is the status of such a binding: Failed, with
// Ready False and Conflict True.
func testAccBindingConflictStatus() map[string]any {
	return map[string]any{
		"phase":   "Failed",
		"message": testAccBindingConflictMessage,
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "SyncConflict", "message": testAccBindingConflictMessage},
			map[string]any{"type": "Conflict", "status": "True", "reason": "SyncConflict", "message": testAccBindingConflictMessage},
		},
	}
}

// testAccCheckBindingGets checks how many binding GETs the mock served.
func testAccCheckBindingGets(mock *mockKupeAPI, check func(gets int) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return check(mock.databaseBindingGets)
	}
}

// TestAccDatabaseBindingResource_CreateWait: Create returns once the binding
// is Ready, and keeps waiting while it is Pending, ending the apply without
// an error when the timeout fires.
func TestAccDatabaseBindingResource_CreateWait(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database_binding.test"
	tests := []struct {
		name      string
		status    map[string]any
		wantPhase string
		wantGets  func(gets int) error
	}{
		{
			name: "ready returns at the first check", wantPhase: "Ready",
			wantGets: func(gets int) error {
				if gets != 1 {
					return fmt.Errorf("binding GETs = %d, want 1", gets)
				}
				return nil
			},
		},
		{
			name:      "pending keeps waiting until the timeout",
			status:    map[string]any{"phase": "Pending", "message": "cluster prod does not exist"},
			wantPhase: "Pending",
			wantGets: func(gets int) error {
				if gets < 2 {
					return fmt.Errorf("binding GETs = %d, want the wait to keep polling", gets)
				}
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			mock.setState(func(m *mockKupeAPI) { m.databaseBindingStatus = tt.status })
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps: []resource.TestStep{
					{
						Config: testAccDatabaseBindingConfig(mock.url(), "prod", "api", "app", `  timeouts = { create = "1s" }`),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr(addr, "phase", tt.wantPhase),
							testAccCheckBindingGets(mock, tt.wantGets),
						),
					},
				},
			})
		})
	}
}

// TestAccDatabaseBindingResource_FailedCreate: a Failed binding ends Create
// at once with an error that says what to change, and stays in state marked
// tainted, so a later apply replaces it and destroy removes it.
func TestAccDatabaseBindingResource_FailedCreate(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database_binding.test"
	// A 1m timeout the fix never waits for: a Failed binding taken for in
	// progress would poll for a minute and then only warn.
	config := func(host string) string {
		return testAccDatabaseBindingConfig(host, "prod", "api", "app", `  timeouts = { create = "1m" }`)
	}
	wantErr := regexp.MustCompile(`(?s)database binding failed.*` + wrappedText(testAccBindingConflictMessage) +
		`.*` + wrappedText(`Namespace "api" of cluster "prod" already has a Service named "orders-db" or a Secret named "orders-db-app"`) +
		`.*` + wrappedText("Rename or remove that object, or, if the Secret is the clash, set a different `secret_name`.") +
		`.*` + wrappedText("marked tainted"))

	t.Run("a fix and apply replaces the binding", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		mock.setState(func(m *mockKupeAPI) { m.databaseBindingStatus = testAccBindingConflictStatus() })
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{Config: config(mock.url()), ExpectError: wantErr},
				{
					PreConfig: func() {
						mock.setState(func(m *mockKupeAPI) {
							if m.databaseBindingGets != 1 {
								t.Errorf("binding GETs before the error = %d, want 1: Create must stop at the first Failed", m.databaseBindingGets)
							}
							// The tenant removed the conflicting object.
							m.databaseBindingStatus = nil
						})
					},
					// The same config plans a replacement: the failed binding
					// is in state, and tainted.
					Config: config(mock.url()),
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
					},
					Check: resource.TestCheckResourceAttr(addr, "phase", "Ready"),
				},
			},
		})
	})

	t.Run("destroy removes the failed binding", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		// The database is outside the config: deleting a database deletes
		// its bindings, which would hide a binding Terraform lost track of.
		mock.setState(func(m *mockKupeAPI) {
			m.databases["orders-db"] = map[string]any{"name": "orders-db"}
			m.databaseBindingStatus = testAccBindingConflictStatus()
		})
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			Steps: []resource.TestStep{{
				Config: testAccProviderConfig(mock.url()) + `
resource "kupe_database_binding" "test" {
  database  = "orders-db"
  cluster   = "prod"
  namespace = "api"
  role      = "app"
  timeouts  = { create = "1m" }
}
`,
				ExpectError: wantErr,
			}},
		})
		// The test case destroys what its state holds, while the binding is
		// still Failed. Not CheckDestroy: it is skipped when the state is
		// empty, which is the failure this guards against.
		mock.setState(func(m *mockKupeAPI) {
			if len(m.databaseBindings) != 0 {
				t.Errorf("bindings left after destroy: %d; the failed binding was not in state", len(m.databaseBindings))
			}
		})
	})
}

func TestDatabaseBindingConflicted(t *testing.T) {
	tests := []struct {
		name   string
		status *client.DatabaseBindingStatus
		want   bool
	}{
		{name: "no status", status: nil, want: false},
		{name: "no conditions", status: &client.DatabaseBindingStatus{Phase: "Failed"}, want: false},
		{
			name: "conflict true",
			status: &client.DatabaseBindingStatus{Conditions: []client.Condition{
				{Type: "Ready", Status: "False", Reason: "SyncConflict"}, {Type: "Conflict", Status: "True", Reason: "SyncConflict"},
			}},
			want: true,
		},
		{
			name: "conflict false",
			status: &client.DatabaseBindingStatus{Conditions: []client.Condition{
				{Type: "Conflict", Status: "False", Reason: "NoConflict"},
			}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseBindingConflicted(tt.status); got != tt.want {
				t.Errorf("databaseBindingConflicted() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMapDatabaseBindingSecretName: secret_name is never null in state. It
// is the requested name, else the platform's effective name, else, before
// the platform has reconciled the binding, its default "{database}-{role}".
func TestMapDatabaseBindingSecretName(t *testing.T) {
	tests := []struct {
		name    string
		binding client.DatabaseBinding
		want    string
	}{
		{
			name:    "requested",
			binding: client.DatabaseBinding{Database: "orders-db", Role: "app", SecretName: "orders-creds"},
			want:    "orders-creds",
		},
		{
			name: "effective, from status",
			binding: client.DatabaseBinding{Database: "orders-db", Role: "app",
				Status: &client.DatabaseBindingStatus{SecretName: "orders-db-app"}},
			want: "orders-db-app",
		},
		{
			name:    "not reconciled yet",
			binding: client.DatabaseBinding{Database: "orders-db", Role: "owner"},
			want:    "orders-db-owner",
		},
		{
			name: "status without a name yet",
			binding: client.DatabaseBinding{Database: "orders-db", Role: "app",
				Status: &client.DatabaseBindingStatus{Phase: "Pending"}},
			want: "orders-db-app",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var state DatabaseBindingResourceModel
			mapDatabaseBindingToState(&tt.binding, &state)
			if state.SecretName.IsNull() || state.SecretName.ValueString() != tt.want {
				t.Errorf("secret_name = %v, want %q", state.SecretName, tt.want)
			}
		})
	}
}
