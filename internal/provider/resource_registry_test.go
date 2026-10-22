package provider

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testRegistry is the HCL a kupe_registry.test config is rendered from.
// Empty strings and nil pointers leave the attribute out.
type testRegistry struct {
	name, storageLimit string
	protection         *bool
	updateTimeout      string
}

func (g testRegistry) hcl() string {
	attrs := ""
	if g.name != "" {
		attrs += fmt.Sprintf("  name = %q\n", g.name)
	}
	if g.storageLimit != "" {
		attrs += fmt.Sprintf("  storage_limit = %q\n", g.storageLimit)
	}
	if g.protection != nil {
		attrs += fmt.Sprintf("  deletion_protection = %t\n", *g.protection)
	}
	if g.updateTimeout != "" {
		attrs += fmt.Sprintf("  timeouts = { update = %q }\n", g.updateTimeout)
	}
	return fmt.Sprintf("\nresource \"kupe_registry\" \"test\" {\n%s}\n", attrs)
}

func testAccRegistryConfig(host string, g testRegistry) string {
	return testAccProviderConfig(host) + g.hcl()
}

// testAccCheckNoRegistry verifies the mock holds no registry, connection or
// token, i.e. destroy really deleted them through the API.
func testAccCheckNoRegistry(mock *mockKupeAPI) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.registries) != 0 || len(mock.registryBindings) != 0 || len(mock.registryTokens) != 0 {
			return fmt.Errorf("expected no registry, connections or tokens, got %d, %d and %d",
				len(mock.registries), len(mock.registryBindings), len(mock.registryTokens))
		}
		return nil
	}
}

func expectRegistryAction(action plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kupe_registry.test", action)},
	}
}

// lastRegistryBody is the body of the last request of method to path.
func lastRegistryBody(t *testing.T, mock *mockKupeAPI, method, path string) map[string]any {
	t.Helper()
	mock.mu.Lock()
	defer mock.mu.Unlock()
	reqs := mock.mockRegistryRequestsOf(method, path)
	if len(reqs) == 0 {
		t.Fatalf("no %s %s request", method, path)
	}
	return reqs[len(reqs)-1].body
}

func TestAccRegistryResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	const addr = "kupe_registry.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistry(mock),
		Steps: []resource.TestStep{
			{
				Config: testAccRegistryConfig(mock.url(), testRegistry{}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "registry"),
					resource.TestCheckResourceAttr(addr, "deletion_protection", "true"),
					resource.TestCheckNoResourceAttr(addr, "storage_limit"),
					resource.TestCheckResourceAttr(addr, "phase", "Running"),
					resource.TestCheckResourceAttr(addr, "endpoint", "registry.kupe.cloud/acme"),
					resource.TestCheckResourceAttr(addr, "storage_limit_bytes", fmt.Sprint(mockPlanRegistryCapBytes)),
					resource.TestCheckResourceAttr(addr, "storage_used_bytes", "1048576"),
					resource.TestCheckResourceAttr(addr, "repository_count", "2"),
					resource.TestCheckResourceAttrSet(addr, "etag"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
					func(*terraform.State) error {
						body := lastRegistryBody(t, mock, "POST", mockRegistriesPath)
						if body["deletionProtection"] != true || body["name"] != "registry" {
							return fmt.Errorf("POST body = %v, want deletionProtection true and name registry", body)
						}
						if _, sent := body["storageLimit"]; sent {
							return fmt.Errorf("POST body = %v, want no storageLimit", body)
						}
						return nil
					},
				),
			},
			{
				Config:           testAccRegistryConfig(mock.url(), testRegistry{storageLimit: "2Gi"}),
				ConfigPlanChecks: expectRegistryAction(plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_limit", "2Gi"),
					resource.TestCheckResourceAttr(addr, "storage_limit_bytes", fmt.Sprint(int64(2)<<30)),
					func(*terraform.State) error {
						body := lastRegistryBody(t, mock, "PATCH", mockRegistriesPath+"/registry")
						if len(body) != 1 || body["storageLimit"] != "2Gi" {
							return fmt.Errorf("PATCH body = %v, want only storageLimit 2Gi", body)
						}
						return nil
					},
				),
			},
			{
				// Removing the limit clears it in place: the plan's applies.
				Config:           testAccRegistryConfig(mock.url(), testRegistry{}),
				ConfigPlanChecks: expectRegistryAction(plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "storage_limit"),
					resource.TestCheckResourceAttr(addr, "storage_limit_bytes", fmt.Sprint(mockPlanRegistryCapBytes)),
					func(*terraform.State) error {
						body := lastRegistryBody(t, mock, "PATCH", mockRegistriesPath+"/registry")
						if v, ok := body["storageLimit"]; !ok || v != "" || len(body) != 1 {
							return fmt.Errorf("PATCH body = %v, want only storageLimit \"\"", body)
						}
						return nil
					},
				),
			},
			{
				ResourceName:                         addr,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "registry",
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// Lift protection so the test's final destroy succeeds.
				Config:           testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)}),
				ConfigPlanChecks: expectRegistryAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr(addr, "deletion_protection", "false"),
			},
		},
	})
}

func TestAccRegistryResource_PlanTimeErrors(t *testing.T) {
	tests := []struct {
		name    string
		config  testRegistry
		wantErr string
	}{
		{name: "another name", config: testRegistry{name: "images"}, wantErr: `a tenant has one registry, named\s+registry`},
		{name: "a decimal unit", config: testRegistry{storageLimit: "10G"}, wantErr: `binary unit`},
		{name: "a unit below Mi", config: testRegistry{storageLimit: "512Ki"}, wantErr: `binary unit`},
		{name: "no unit", config: testRegistry{storageLimit: "1024"}, wantErr: `binary unit`},
		{name: "zero", config: testRegistry{storageLimit: "0Gi"}, wantErr: `more than zero`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []resource.TestStep{{
					Config:      testAccRegistryConfig(mock.url(), tt.config),
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
			if len(mock.registryRequests) != 0 {
				t.Errorf("the plan reached the API: %d registry requests", len(mock.registryRequests))
			}
		})
	}
}

func TestAccRegistryResource_DeletionProtectionBlocksDestroy(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	refused := "(?s)registry has deletion protection enabled.*`deletion_protection\\s+=\\s+false`.*" +
		wrappedText(sharedRegistryDeleteNotice)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistry(mock),
		Steps: []resource.TestStep{
			{Config: testAccRegistryConfig(mock.url(), testRegistry{})},
			{
				Config:      testAccProviderConfig(mock.url()),
				ExpectError: regexp.MustCompile(refused),
			},
			{
				// The refused delete left the registry in state: lifting
				// protection is an in-place update, not a create. The
				// platform has not caught up yet (Provisioning, an old
				// generation) and the update timeout is 1s: a change to
				// deletion_protection alone is not waited for, so it passes.
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.registryStatuses = []map[string]any{{"phase": "Provisioning", "observedGeneration": 0}}
					})
				},
				Config:           testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false), updateTimeout: "1s"}),
				ConfigPlanChecks: expectRegistryAction(plancheck.ResourceActionUpdate),
				Check: func(*terraform.State) error {
					body := lastRegistryBody(t, mock, "PATCH", mockRegistriesPath+"/registry")
					if len(body) != 1 || body["deletionProtection"] != false {
						return fmt.Errorf("PATCH body = %v, want only deletionProtection false", body)
					}
					return nil
				},
			},
		},
	})
}

// sharedRegistryDeleteNotice is the wording for what deleting a registry does
// to its images. Every Kupe surface states it verbatim.
const sharedRegistryDeleteNotice = "Deleting a registry deletes every image in it. They cannot be restored."

func TestRegistryDeleteNotice(t *testing.T) {
	if registryDeleteNotice != sharedRegistryDeleteNotice {
		t.Errorf("registryDeleteNotice = %q, want the shared wording %q", registryDeleteNotice, sharedRegistryDeleteNotice)
	}
	doc, err := os.ReadFile("../../docs/resources/registry.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), sharedRegistryDeleteNotice) {
		t.Errorf("docs/resources/registry.md does not state %q; run make docs", sharedRegistryDeleteNotice)
	}
}

func TestAccRegistryResource_ImportRejectsOtherNames(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistry(mock),
		Steps: []resource.TestStep{
			{Config: testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)})},
			{
				ResourceName:  "kupe_registry.test",
				ImportState:   true,
				ImportStateId: "images",
				ExpectError:   regexp.MustCompile(`a tenant has one registry, named registry`),
			},
		},
	})
}

// TestAccRegistryResource_SecondRegistry: a tenant that already has its
// registry gets the import hint, not a bare conflict.
func TestAccRegistryResource_SecondRegistry(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) {
		m.registries["registry"] = map[string]any{
			"name": "registry", "storageLimit": "", "deletionProtection": true, "generation": 1,
			"status": map[string]any{}, "resourceVersion": "1", "createdAt": "2026-10-21T10:00:00Z",
		}
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{{
			Config: testAccRegistryConfig(mock.url(), testRegistry{}),
			ExpectError: regexp.MustCompile(`(?s)Error code: REGISTRY_ALREADY_EXISTS.*` +
				wrappedText("A tenant has one registry. Import it: `terraform import kupe_registry.this registry`.")),
		}},
	})
}

// TestAccRegistryResource_RegistriesUnavailable: with the registry switched
// off, kupe-api still serves reads, deletes and a PATCH of deletion
// protection alone. Refresh keeps the registry with no change planned, a
// storage change fails with the hint, protection can be lifted and the
// registry destroyed.
func TestAccRegistryResource_RegistriesUnavailable(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistry(mock),
		Steps: []resource.TestStep{
			{Config: testAccRegistryConfig(mock.url(), testRegistry{})},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.registriesDisabled = true }) },
				Config:    testAccRegistryConfig(mock.url(), testRegistry{}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("kupe_registry.test", "phase", "Running"),
			},
			{
				Config:           testAccRegistryConfig(mock.url(), testRegistry{storageLimit: "2Gi"}),
				ConfigPlanChecks: expectRegistryAction(plancheck.ResourceActionUpdate),
				ExpectError: regexp.MustCompile(`(?s)failed to update registry.*` +
					wrappedText("Registries are not enabled for this Kupe Cloud environment.") + `.*` +
					wrappedText("Terraform state is kept unchanged.")),
			},
			{
				Config:           testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)}),
				ConfigPlanChecks: expectRegistryAction(plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("kupe_registry.test", "deletion_protection", "false"),
			},
			{
				Config: testAccProviderConfig(mock.url()),
				Check:  testAccCheckNoRegistry(mock),
			},
		},
	})
}

// TestAccRegistryResource_CreateWhileUnavailable: creating while the
// registry is switched off fails with the hint.
func TestAccRegistryResource_CreateWhileUnavailable(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) { m.registriesDisabled = true })
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{{
			Config: testAccRegistryConfig(mock.url(), testRegistry{}),
			ExpectError: regexp.MustCompile(`(?s)failed to create registry.*Error code: registries_unavailable.*` +
				wrappedText("Registries are not enabled")),
		}},
	})
}

// TestAccRegistryResource_ReadsUnavailableKeepState: a read refused with
// registries_unavailable is terminal, never "gone": refresh fails and the
// registry stays in state, so the next plan once reads work again is empty.
func TestAccRegistryResource_ReadsUnavailableKeepState(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistry(mock),
		Steps: []resource.TestStep{
			{Config: testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)})},
			{
				PreConfig:   func() { mock.setState(func(m *mockKupeAPI) { m.registryReadsUnavailable = true }) },
				Config:      testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)}),
				ExpectError: regexp.MustCompile(`(?s)failed to read registry.*` + wrappedText("Registries are not enabled")),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.registryReadsUnavailable = false }) },
				Config:    testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccRegistryResource_APIWithoutRegistries: a kupe-api older than
// registries answers every registry route with the router's plain 404. Create
// says so, and a refresh of a registry already in state fails instead of
// dropping it.
func TestAccRegistryResource_APIWithoutRegistries(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		mock.setState(func(m *mockKupeAPI) { m.registryRoutesMissing = true })
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			Steps: []resource.TestStep{{
				Config:      testAccRegistryConfig(mock.url(), testRegistry{}),
				ExpectError: regexp.MustCompile(`(?s)failed to create registry.*` + wrappedText("does not offer registries yet")),
			}},
		})
	})
	t.Run("refresh keeps state", func(t *testing.T) {
		mock := newMockKupeAPI()
		defer mock.close()
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
			CheckDestroy:             testAccCheckNoRegistry(mock),
			Steps: []resource.TestStep{
				{Config: testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)})},
				{
					PreConfig:   func() { mock.setState(func(m *mockKupeAPI) { m.registryRoutesMissing = true }) },
					Config:      testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)}),
					ExpectError: regexp.MustCompile(`(?s)failed to read registry.*` + wrappedText("does not offer registries yet")),
				},
				{
					PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.registryRoutesMissing = false }) },
					Config:    testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)}),
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
					},
				},
			},
		})
	})
}

// TestAccRegistryResource_DeleteWaitsUntilGone: the registry keeps being
// found (Terminating) for a few reads after the DELETE is accepted; destroy
// waits until it is gone. The mock removes it only on the read that answers
// 404, so CheckDestroy passes only if the provider polled until then.
func TestAccRegistryResource_DeleteWaitsUntilGone(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) { m.registryDeleteLag = 3 })
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistry(mock),
		Steps: []resource.TestStep{
			{Config: testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)})},
		},
	})
}

// TestAccRegistryResource_TerminatingFailsCreate: a registry that is being
// deleted fails the create wait at once.
func TestAccRegistryResource_TerminatingFailsCreate(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) {
		m.registryStatuses = []map[string]any{{"phase": "Terminating"}}
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{{
			Config:      testAccRegistryConfig(mock.url(), testRegistry{protection: boolPtr(false)}),
			ExpectError: regexp.MustCompile(`(?s)registry terminating.*being deleted`),
		}},
	})
}
