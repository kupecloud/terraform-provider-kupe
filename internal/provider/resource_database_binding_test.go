package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// testAccDatabaseBindingConfig renders an unprotected database plus one
// binding with the given extra binding attributes.
func testAccDatabaseBindingConfig(host, cluster string, namespaces []string, role, extra string) string {
	return testAccDatabaseConfig(host, testDatabase{protection: boolPtr(false)}) + fmt.Sprintf(`
resource "kupe_database_binding" "test" {
  database   = kupe_database.test.name
  cluster    = %q
  namespaces = %s
  role       = %q
%s
}
`, cluster, hclStrings(namespaces), role, extra)
}

// hclStrings is values as an HCL list literal.
func hclStrings(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// ns is a namespace list, for short test configs.
func ns(names ...string) []string { return names }

// testAccCheckBindingNamespaceStatus checks namespace_status lists exactly
// these namespaces, in this order, each Ready with its host.
func testAccCheckBindingNamespaceStatus(addr string, names ...string) resource.TestCheckFunc {
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr(addr, "namespace_status.#", fmt.Sprint(len(names))),
	}
	for i, name := range names {
		prefix := fmt.Sprintf("namespace_status.%d.", i)
		checks = append(checks,
			resource.TestCheckResourceAttr(addr, prefix+"name", name),
			resource.TestCheckResourceAttr(addr, prefix+"host", "orders-db."+name+".svc.cluster.local"),
			resource.TestCheckResourceAttr(addr, prefix+"phase", "Ready"),
			resource.TestCheckResourceAttr(addr, prefix+"message", ""),
		)
	}
	return resource.ComposeAggregateTestCheckFunc(checks...)
}

// testAccCheckBindingRequests checks the binding POST and PATCH bodies the
// mock received.
func testAccCheckBindingRequests(mock *mockKupeAPI, check func(reqs []mockRequest) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return check(mock.databaseBindingRequests)
	}
}

// testAccWantBindingRequests checks the bodies were, in order, these
// methods with these namespace lists, and that each PATCH sent If-Match.
func testAccWantBindingRequests(mock *mockKupeAPI, want ...string) resource.TestCheckFunc {
	return testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
		got := make([]string, 0, len(reqs))
		for _, req := range reqs {
			got = append(got, fmt.Sprintf("%s %v", req.method, req.body["namespaces"]))
			if req.method == "PATCH" && req.ifMatch == "" {
				return fmt.Errorf("PATCH without If-Match: %v", req.body)
			}
		}
		if strings.Join(got, "; ") != strings.Join(want, "; ") {
			return fmt.Errorf("binding requests = %q, want %q", got, want)
		}
		return nil
	})
}

func TestAccDatabaseBindingResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database_binding.test"
	replace := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
	}
	inPlace := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
	}
	noop := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)},
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The default name leaves the namespaces out: they can change.
					resource.TestCheckResourceAttr(addr, "name", "orders-db-prod-readwrite"),
					resource.TestCheckResourceAttr(addr, "database", "orders-db"),
					resource.TestCheckResourceAttr(addr, "secret_name", "orders-db-readwrite"),
					resource.TestCheckResourceAttr(addr, "namespaces.#", "1"),
					resource.TestCheckTypeSetElemAttr(addr, "namespaces.*", "api"),
					testAccCheckBindingNamespaceStatus(addr, "api"),
					resource.TestCheckResourceAttr(addr, "phase", "Ready"),
					resource.TestCheckResourceAttrSet(addr, "etag"),
					testAccWantBindingRequests(mock, "POST [api]"),
				),
			},
			{
				ResourceName:                         addr,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "orders-db/orders-db-prod-readwrite",
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// Adding a namespace updates the binding in place, with the
				// complete new list, sorted.
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker", "api"), "readwrite", ""),
				ConfigPlanChecks: inPlace,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "orders-db-prod-readwrite"),
					resource.TestCheckResourceAttr(addr, "namespaces.#", "2"),
					testAccCheckBindingNamespaceStatus(addr, "api", "worker"),
					resource.TestCheckResourceAttr(addr, "phase", "Ready"),
					testAccWantBindingRequests(mock, "POST [api]", "PATCH [api worker]"),
				),
			},
			{
				// The same namespaces in another order are the same set.
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "worker"), "readwrite", ""),
				ConfigPlanChecks: noop,
			},
			{
				// Removing one updates in place too.
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readwrite", ""),
				ConfigPlanChecks: inPlace,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBindingNamespaceStatus(addr, "worker"),
					testAccWantBindingRequests(mock, "POST [api]", "PATCH [api worker]", "PATCH [worker]"),
				),
			},
			{
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readwrite", `  secret_name = "orders-credentials"`),
				ConfigPlanChecks: replace,
				Check:            resource.TestCheckResourceAttr(addr, "secret_name", "orders-credentials"),
			},
			{
				// An explicit name that differs from the current one
				// replaces the binding too.
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readwrite",
					"  secret_name = \"orders-credentials\"\n  name = \"orders-worker\""),
				ConfigPlanChecks: replace,
				Check:            resource.TestCheckResourceAttr(addr, "name", "orders-worker"),
			},
			{
				// Removing name from config keeps the existing binding.
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readwrite", `  secret_name = "orders-credentials"`),
				ConfigPlanChecks: noop,
				Check:            resource.TestCheckResourceAttr(addr, "name", "orders-worker"),
			},
			{
				// Removing secret_name keeps the current name too: it does
				// not go back to the default.
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readwrite", ""),
				ConfigPlanChecks: noop,
				Check:            resource.TestCheckResourceAttr(addr, "secret_name", "orders-credentials"),
			},
			{
				// Writing the default name out replaces the binding with it.
				Config:           testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readwrite", `  secret_name = "orders-db-readwrite"`),
				ConfigPlanChecks: replace,
				Check:            resource.TestCheckResourceAttr(addr, "secret_name", "orders-db-readwrite"),
			},
			{
				// A new cluster replaces the binding, and the default name
				// follows it.
				Config:           testAccDatabaseBindingConfig(mock.url(), "staging", ns("worker"), "readwrite", ""),
				ConfigPlanChecks: replace,
				Check:            resource.TestCheckResourceAttr(addr, "name", "orders-db-staging-readwrite"),
			},
		},
	})
}

// TestAccDatabaseBindingResource_ReadOnly: a readonly binding takes its
// default name and Secret name from the role, and changing the role
// replaces the binding: the platform cannot change a binding's role.
func TestAccDatabaseBindingResource_ReadOnly(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database_binding.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readonly", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "orders-db-prod-readonly"),
					resource.TestCheckResourceAttr(addr, "role", "readonly"),
					resource.TestCheckResourceAttr(addr, "secret_name", "orders-db-readonly"),
					resource.TestCheckResourceAttr(addr, "phase", "Ready"),
				),
			},
			{
				ResourceName:                         addr,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "orders-db/orders-db-prod-readonly",
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readwrite", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "orders-db-prod-readwrite"),
					resource.TestCheckResourceAttr(addr, "role", "readwrite"),
					resource.TestCheckResourceAttr(addr, "secret_name", "orders-db-readwrite"),
				),
			},
			{
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("worker"), "readonly", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.TestCheckResourceAttr(addr, "role", "readonly"),
			},
		},
	})
}

func TestAccDatabaseBindingResource_PlanTimeErrors(t *testing.T) {
	tests := []struct {
		name       string
		cluster    string
		namespaces []string
		role       string
		// wantErr is "" for a row that plans without an error.
		wantErr string
	}{
		// app gets the same standard diagnostic as any other value that is
		// not a role.
		{name: "role app", cluster: "prod", namespaces: ns("api"), role: "app", wantErr: testAccRoleNotOneOf("app")},
		{name: "unknown role", cluster: "prod", namespaces: ns("api"), role: "admin", wantErr: testAccRoleNotOneOf("admin")},
		{
			name: "default name longer than 63 characters", cluster: strings.Repeat("c", 50),
			namespaces: ns("api"), role: "readwrite", wantErr: `Default binding name too long`,
		},
		// readwrite is four characters longer than owner: a default name of
		// 61 characters with owner is 65 with readwrite.
		{name: "default name fits with owner", cluster: strings.Repeat("c", 45), namespaces: ns("api"), role: "owner"},
		{
			name: "the same default name too long with readwrite", cluster: strings.Repeat("c", 45),
			namespaces: ns("api"), role: "readwrite", wantErr: `Default binding name too long`,
		},
		{
			name: "no namespace", cluster: "prod", namespaces: ns(), role: "readwrite",
			wantErr: wrappedText(`Attribute namespaces set must contain at least 1 elements and at most 10 elements, got: 0`),
		},
		{name: "ten namespaces", cluster: "prod", namespaces: testNamespaces(10), role: "readwrite"},
		{
			name: "eleven namespaces", cluster: "prod", namespaces: testNamespaces(11), role: "readwrite",
			wantErr: wrappedText(`Attribute namespaces set must contain at least 1 elements and at most 10 elements, got: 11`),
		},
		{name: "kube-* namespace", cluster: "prod", namespaces: ns("api", "kube-system"), role: "readwrite", wantErr: `Reserved namespace`},
		{name: "argocd namespace", cluster: "prod", namespaces: ns("argocd"), role: "readwrite", wantErr: `Reserved namespace`},
		{name: "namespace with uppercase", cluster: "prod", namespaces: ns("api", "API"), role: "readwrite", wantErr: `must be lowercase letters`},
		{name: "cluster with a double hyphen", cluster: "prod--eu", namespaces: ns("api"), role: "readwrite", wantErr: `not contain '--'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			step := resource.TestStep{Config: testAccDatabaseBindingConfig(mock.url(), tt.cluster, tt.namespaces, tt.role, "")}
			if tt.wantErr == "" {
				// The row plans: no error.
				step.PlanOnly, step.ExpectNonEmptyPlan = true, true
			} else {
				step.ExpectError = regexp.MustCompile(tt.wantErr)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps:                    []resource.TestStep{step},
			})
		})
	}
}

// testNamespaces is n distinct namespace names.
func testNamespaces(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("ns%d", i)
	}
	return names
}

// testAccRoleNotOneOf matches the framework's standard OneOf diagnostic for
// a role that is not one of the three.
func testAccRoleNotOneOf(role string) string {
	return wrappedText(fmt.Sprintf(`Attribute role value must be one of: ["owner" "readwrite" "readonly"], got: %q`, role))
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
  database   = "orders--db"
  cluster    = "prod"
  namespaces = ["api"]
  role       = "readwrite"
}
`,
				ExpectError: regexp.MustCompile(`not contain '--'`),
			},
		},
	})
}

// TestAccDatabaseBindingResource_DatabaseRefAllowsOlderLongNames: a binding
// may name a database created before the platform's 44-character limit, so
// the reference is held only to the Kubernetes name limit.
func TestAccDatabaseBindingResource_DatabaseRefAllowsOlderLongNames(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{{
			Config: testAccProviderConfig(mock.url()) + fmt.Sprintf(`
resource "kupe_database_binding" "test" {
  name       = "legacy-db-prod-readwrite"
  database   = %q
  cluster    = "prod"
  namespaces = ["api"]
  role       = "readwrite"
}
`, "d"+strings.Repeat("b", 51)),
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

func TestParseDatabaseBindingImportID(t *testing.T) {
	tests := []struct {
		id                     string
		wantDatabase, wantName string
		wantOK                 bool
	}{
		{id: "orders-db/orders-db-prod-readwrite", wantDatabase: "orders-db", wantName: "orders-db-prod-readwrite", wantOK: true},
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

// testAccBindingConflictMessage is the message the platform sets on a
// namespace where a tenant object already holds the binding's Service or
// Secret name.
func testAccBindingConflictMessage(namespace string) string {
	return fmt.Sprintf("namespace %q already has a Secret named %q that the platform did not create; remove it, "+
		"or bind with another secretName", namespace, "orders-db-readwrite")
}

// testAccBindingConflict is the status of such a namespace, for
// databaseBindingNamespaceStatus.
func testAccBindingConflict(namespace string) map[string]any {
	return map[string]any{"phase": "Failed", "host": "", "message": testAccBindingConflictMessage(namespace)}
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
// is Ready, and keeps waiting while it is Pending, failing the apply when the
// timeout fires.
func TestAccDatabaseBindingResource_CreateWait(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database_binding.test"
	tests := []struct {
		name      string
		status    map[string]any
		wantPhase string
		wantErr   string // empty: the create applies
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
			name:   "pending keeps waiting until the timeout, then fails",
			status: map[string]any{"phase": "Pending", "message": "cluster prod does not exist"},
			wantErr: `(?s)database binding create timed out while Pending.*` +
				wrappedText(`If the cluster "prod" does not exist yet, the binding becomes Ready once it does.`) +
				`.*` + wrappedText("Status message: cluster prod does not exist") +
				`.*` + wrappedText("marked tainted"),
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
			step := resource.TestStep{
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", `  timeouts = { create = "1s" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "phase", tt.wantPhase),
					testAccCheckBindingGets(mock, tt.wantGets),
				),
			}
			if tt.wantErr != "" {
				step.Check, step.ExpectError = nil, regexp.MustCompile(tt.wantErr)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoDatabases(mock),
				Steps:                    []resource.TestStep{step},
			})
			if tt.wantErr != "" {
				if err := testAccCheckBindingGets(mock, tt.wantGets)(nil); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

// TestAccDatabaseBindingResource_FailedCreate: a binding with a Failed
// namespace ends Create at once with an error that says what to change, and
// stays in state marked tainted, so a later apply replaces it and destroy
// removes it.
func TestAccDatabaseBindingResource_FailedCreate(t *testing.T) {
	shrinkPollInterval(t)
	const addr = "kupe_database_binding.test"
	// A 1m timeout the fix never waits for: a Failed binding taken for in
	// progress would poll for a minute and then only warn.
	config := func(host string) string {
		return testAccDatabaseBindingConfig(host, "prod", ns("api", "jobs"), "readwrite", `  timeouts = { create = "1m" }`)
	}
	wantErr := regexp.MustCompile(`(?s)database binding failed.*` + wrappedText(testAccBindingConflictMessage("jobs")) +
		`.*` + wrappedText(`Namespace "jobs" of cluster "prod" already has a Service or Endpoints object named "orders-db", or a Secret named "orders-db-readwrite", that`) +
		`.*` + wrappedText("Rename or remove that object, or, if the Secret is the clash, set a different `secret_name`.") +
		`.*` + wrappedText("marked tainted"))

	t.Run("a fix and apply replaces the binding", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		mock.setState(func(m *mockKupeAPI) {
			m.databaseBindingNamespaceStatus = map[string]map[string]any{"jobs": testAccBindingConflict("jobs")}
		})
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
							m.databaseBindingNamespaceStatus = nil
						})
					},
					// The same config plans a replacement: the failed binding
					// is in state, and tainted.
					Config: config(mock.url()),
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
					},
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(addr, "phase", "Ready"),
						testAccCheckBindingNamespaceStatus(addr, "api", "jobs"),
					),
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
			m.databaseBindingNamespaceStatus = map[string]map[string]any{"jobs": testAccBindingConflict("jobs")}
		})
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			Steps: []resource.TestStep{{
				Config: testAccProviderConfig(mock.url()) + `
resource "kupe_database_binding" "test" {
  database   = "orders-db"
  cluster    = "prod"
  namespaces = ["api", "jobs"]
  role       = "readwrite"
  timeouts   = { create = "1m" }
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

// TestAccDatabaseBindingResource_UpdateWaitsForTheNewList: right after the
// PATCH the binding is still Ready with the status of the namespaces it had
// before. Update waits until the status describes the new list, both when a
// namespace is added and when one is removed.
func TestAccDatabaseBindingResource_UpdateWaitsForTheNewList(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database_binding.test"
	var getsBefore int
	countGets := func() {
		mock.setState(func(m *mockKupeAPI) {
			m.databaseBindingLagOnPatch = 2
			getsBefore = m.databaseBindingGets
		})
	}
	// Each step's own reads: the refresh before the plan, the plan's and
	// the check's. The wait adds at least lag + 1.
	wantWaited := testAccCheckBindingGets(mock, func(gets int) error {
		if gets-getsBefore < 3 {
			return fmt.Errorf("binding GETs during the update = %d, want at least 3: Update did not wait for the new list", gets-getsBefore)
		}
		return nil
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", "")},
			{
				PreConfig: countGets,
				Config:    testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "jobs"), "readwrite", ""),
				Check:     resource.ComposeAggregateTestCheckFunc(testAccCheckBindingNamespaceStatus(addr, "api", "jobs"), wantWaited),
			},
			{
				PreConfig: countGets,
				Config:    testAccDatabaseBindingConfig(mock.url(), "prod", ns("jobs"), "readwrite", ""),
				Check:     resource.ComposeAggregateTestCheckFunc(testAccCheckBindingNamespaceStatus(addr, "jobs"), wantWaited),
			},
		},
	})
}

// TestAccDatabaseBindingResource_UpdateFailedNamespace: a namespace added
// that already holds a Service or Secret of the binding's names ends Update
// with an error naming it. The new list is recorded, so the same config
// plans nothing; taking the namespace out again updates the binding.
func TestAccDatabaseBindingResource_UpdateFailedNamespace(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database_binding.test"
	withJobs := testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "jobs"), "readwrite", `  timeouts = { update = "1m" }`)
	wantErr := regexp.MustCompile(`(?s)database binding failed.*` + wrappedText(testAccBindingConflictMessage("jobs")) +
		`.*` + wrappedText(`Namespace "jobs" of cluster "prod" already has a Service or Endpoints object named "orders-db"`) +
		`.*` + wrappedText("The other namespaces keep working. "+changeRecordedNotice))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", `  timeouts = { update = "1m" }`)},
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.databaseBindingNamespaceStatus = map[string]map[string]any{"jobs": testAccBindingConflict("jobs")}
					})
				},
				Config:      withJobs,
				ExpectError: wantErr,
			},
			{
				Config:           withJobs,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "namespaces.#", "2"),
					resource.TestCheckResourceAttr(addr, "phase", "Failed"),
					resource.TestCheckResourceAttr(addr, "namespace_status.1.name", "jobs"),
					resource.TestCheckResourceAttr(addr, "namespace_status.1.phase", "Failed"),
					resource.TestCheckResourceAttr(addr, "namespace_status.1.message", testAccBindingConflictMessage("jobs")),
				),
			},
			{
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", `  timeouts = { update = "1m" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBindingNamespaceStatus(addr, "api"),
					resource.TestCheckResourceAttr(addr, "phase", "Ready"),
				),
			},
		},
	})
}

// TestAccDatabaseBindingResource_RemovingAFailedNamespace: removing the
// namespace that failed is how a tenant can fix it. Until the platform has
// withdrawn it the status still says Failed; Update does not take that for a
// failure of the new list.
func TestAccDatabaseBindingResource_RemovingAFailedNamespace(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database_binding.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "jobs"), "readwrite", `  timeouts = { update = "1m" }`)},
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						// Someone created a Secret of the binding's name in jobs.
						m.databaseBindingNamespaceStatus = map[string]map[string]any{"jobs": testAccBindingConflict("jobs")}
						m.databaseBindingLagOnPatch = 2
					})
				},
				Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", `  timeouts = { update = "1m" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBindingNamespaceStatus(addr, "api"),
					resource.TestCheckResourceAttr(addr, "phase", "Ready"),
				),
			},
		},
	})
}

// TestAccDatabaseBindingResource_UpdateStaleETag: the platform's status
// writes change the binding's ETag, so a 412 is retried once with a fresh
// one when the namespaces are unchanged; when another client changed them,
// the update is not applied.
func TestAccDatabaseBindingResource_UpdateStaleETag(t *testing.T) {
	t.Run("only the status moved", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", "")},
				{
					PreConfig: func() {
						mock.setState(func(m *mockKupeAPI) {
							m.databaseBindingBeforePatch = []func(map[string]any){func(map[string]any) {}}
						})
					},
					Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "jobs"), "readwrite", ""),
					Check: resource.ComposeAggregateTestCheckFunc(
						testAccCheckBindingNamespaceStatus("kupe_database_binding.test", "api", "jobs"),
						testAccCheckBindingRequests(mock, func(reqs []mockRequest) error {
							patches := mock.mockBindingPatches()
							if len(patches) != 2 || patches[0].ifMatch == patches[1].ifMatch {
								return fmt.Errorf("PATCHes = %+v, want one retry with a fresh If-Match", patches)
							}
							return nil
						}),
					),
				},
			},
		})
	})

	t.Run("another client changed the namespaces", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoDatabases(mock),
			Steps: []resource.TestStep{
				{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", "")},
				{
					PreConfig: func() {
						mock.setState(func(m *mockKupeAPI) {
							m.databaseBindingBeforePatch = []func(map[string]any){func(b map[string]any) {
								b["namespaces"] = []string{"api", "reports"}
							}}
						})
					},
					Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "jobs"), "readwrite", ""),
					ExpectError: regexp.MustCompile(`(?s)database binding changed outside this apply.*` +
						wrappedText(`(namespaces now differs from state), so the update was not applied.`)),
				},
			},
		})
	})
}

// TestAccDatabaseBindingResource_UpdateWhileDatabasesDisabled: while the
// platform has managed databases switched off, kupe-api refuses a binding
// PATCH; the update fails with the explanation and state keeps the old list.
func TestAccDatabaseBindingResource_UpdateWhileDatabasesDisabled(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_database_binding.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", "")},
			{
				PreConfig:   func() { mock.setState(func(m *mockKupeAPI) { m.databasesDisabled = true }) },
				Config:      testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "jobs"), "readwrite", ""),
				ExpectError: regexp.MustCompile(`(?s)failed to update database binding.*` + wrappedText("Databases are not enabled")),
			},
			{
				// Still off: refresh and plan work, and the plan still has
				// the change to make.
				Config:             testAccDatabaseBindingConfig(mock.url(), "prod", ns("api", "jobs"), "readwrite", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.databasesDisabled = false }) },
				Config:    testAccDatabaseBindingConfig(mock.url(), "prod", ns("api"), "readwrite", ""),
				Check:     testAccCheckBindingNamespaceStatus(addr, "api"),
			},
		},
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

// testBinding is a binding listing namespaces with these status entries,
// given as "name=Phase", and the binding phase.
func testBinding(namespaces []string, phase string, entries ...string) *client.DatabaseBinding {
	status := &client.DatabaseBindingStatus{Phase: phase}
	for _, e := range entries {
		name, entryPhase, _ := strings.Cut(e, "=")
		status.Namespaces = append(status.Namespaces, client.DatabaseBindingNamespaceStatus{Name: name, Phase: entryPhase})
	}
	return &client.DatabaseBinding{Name: "b", Database: "orders-db", Namespaces: namespaces, Status: status}
}

func TestDatabaseBindingDelivered(t *testing.T) {
	tests := []struct {
		name    string
		binding *client.DatabaseBinding
		want    bool
	}{
		{name: "every listed namespace ready", binding: testBinding(ns("api", "jobs"), "Ready", "jobs=Ready", "api=Ready"), want: true},
		{name: "no status yet", binding: &client.DatabaseBinding{Namespaces: ns("api")}, want: false},
		{name: "a namespace added is not in the status yet", binding: testBinding(ns("api", "jobs"), "Ready", "api=Ready"), want: false},
		{name: "a namespace removed is still being withdrawn", binding: testBinding(ns("api"), "Ready", "api=Ready", "jobs=Ready"), want: false},
		{name: "a namespace swapped for another", binding: testBinding(ns("api", "jobs"), "Ready", "api=Ready", "old=Ready"), want: false},
		{name: "a listed namespace pending", binding: testBinding(ns("api", "jobs"), "Ready", "api=Ready", "jobs=Pending"), want: false},
		{name: "binding pending", binding: testBinding(ns("api"), "Pending", "api=Ready"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseBindingDelivered(tt.binding); got != tt.want {
				t.Errorf("databaseBindingDelivered() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDatabaseBindingFailedNamespaces(t *testing.T) {
	tests := []struct {
		name    string
		binding *client.DatabaseBinding
		want    []string
	}{
		{name: "nil", binding: nil, want: nil},
		{name: "none failed", binding: testBinding(ns("api"), "Ready", "api=Ready"), want: nil},
		{name: "listed namespaces, sorted", binding: testBinding(ns("api", "b", "a"), "Failed", "b=Failed", "api=Ready", "a=Failed"), want: ns("a", "b")},
		{name: "a namespace removed does not count", binding: testBinding(ns("api"), "Failed", "api=Ready", "jobs=Failed"), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseBindingFailedNamespaces(tt.binding); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("databaseBindingFailedNamespaces() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDatabaseBindingFailedDetail: several failed namespaces are named
// together, and the detail says what Terraform does next after create and
// after update.
func TestDatabaseBindingFailedDetail(t *testing.T) {
	m := &DatabaseBindingResourceModel{}
	mapDatabaseBindingToState(&client.DatabaseBinding{
		Name: "orders-db-prod-readwrite", Database: "orders-db", Cluster: "prod", Role: "readwrite",
	}, "", m)
	b := testBinding(ns("api", "jobs", "reports"), "Failed", "api=Ready", "jobs=Failed", "reports=Failed")
	b.Status.Message = "conflicts"
	b.Status.Conditions = []client.Condition{{Type: "Conflict", Status: "True", Reason: "SyncConflict"}}

	create := databaseBindingFailedDetail("create", m, b)
	for _, want := range []string{
		`Binding "orders-db-prod-readwrite" of database "orders-db" failed.`,
		"Status message: conflicts",
		`Namespaces "jobs" and "reports" of cluster "prod" each already have a Service or Endpoints object named "orders-db", or a Secret named "orders-db-readwrite", that`,
		"Rename or remove those objects, or, if a Secret is the clash, set a different `secret_name`.",
		"marked tainted",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create detail lacks %q:\n%s", want, create)
		}
	}
	update := databaseBindingFailedDetail("update", m, b)
	if !strings.HasSuffix(update, "The other namespaces keep working. "+changeRecordedNotice) || strings.Contains(update, "tainted") {
		t.Errorf("update detail should end with the recorded-change notice and not mention tainting:\n%s", update)
	}
}

func TestQuotedList(t *testing.T) {
	for _, tt := range []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{ns("a"), `"a"`},
		{ns("a", "b"), `"a" and "b"`},
		{ns("a", "b", "c"), `"a", "b" and "c"`},
	} {
		if got := quotedList(tt.names); got != tt.want {
			t.Errorf("quotedList(%v) = %s, want %s", tt.names, got, tt.want)
		}
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
			binding: client.DatabaseBinding{Database: "orders-db", Role: "readwrite", SecretName: "orders-creds"},
			want:    "orders-creds",
		},
		{
			name: "effective, from status",
			binding: client.DatabaseBinding{Database: "orders-db", Role: "readwrite",
				Status: &client.DatabaseBindingStatus{SecretName: "orders-db-readwrite"}},
			want: "orders-db-readwrite",
		},
		{
			name:    "not reconciled yet",
			binding: client.DatabaseBinding{Database: "orders-db", Role: "owner"},
			want:    "orders-db-owner",
		},
		{
			name: "status without a name yet",
			binding: client.DatabaseBinding{Database: "orders-db", Role: "readwrite",
				Status: &client.DatabaseBindingStatus{Phase: "Pending"}},
			want: "orders-db-readwrite",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var state DatabaseBindingResourceModel
			mapDatabaseBindingToState(&tt.binding, "", &state)
			if state.SecretName.IsNull() || state.SecretName.ValueString() != tt.want {
				t.Errorf("secret_name = %v, want %q", state.SecretName, tt.want)
			}
		})
	}
}

// TestMapDatabaseBindingNamespaceStatus: namespace_status lists every
// namespace in the status, sorted by name, a namespace being withdrawn
// included; it is an empty list, never null, before the platform reports
// any.
func TestMapDatabaseBindingNamespaceStatus(t *testing.T) {
	var state DatabaseBindingResourceModel
	mapDatabaseBindingToState(&client.DatabaseBinding{Database: "orders-db", Role: "readwrite", Namespaces: ns("api")}, `"7"`, &state)
	if state.NamespaceStatus.IsNull() || len(state.NamespaceStatus.Elements()) != 0 || state.ETag.ValueString() != `"7"` {
		t.Errorf("before any status: namespace_status = %v, etag = %v; want an empty list and the ETag", state.NamespaceStatus, state.ETag)
	}

	b := testBinding(ns("jobs", "api"), "Pending", "jobs=Pending", "withdrawn=Ready", "api=Ready")
	mapDatabaseBindingToState(b, "", &state)
	var names []string
	for _, elem := range state.NamespaceStatus.Elements() {
		names = append(names, elem.(types.Object).Attributes()["name"].(types.String).ValueString())
	}
	if fmt.Sprint(names) != "[api jobs withdrawn]" {
		t.Errorf("namespace_status names = %v, want [api jobs withdrawn]", names)
	}
	if fmt.Sprint(setStrings(state.Namespaces)) != "[api jobs]" {
		t.Errorf("namespaces = %v, want [api jobs]", state.Namespaces)
	}
}
