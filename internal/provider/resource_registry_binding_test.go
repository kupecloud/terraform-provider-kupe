package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const bindingAddr = "kupe_registry_binding.test"

// testAccRegistryBindingConfig is a connection of cluster with pushNamespaces
// (nil leaves the attribute out) and extra attributes. The registry is
// seeded in the mock, so the config holds the connection only.
func testAccRegistryBindingConfig(host, cluster string, pushNamespaces []string, extra string) string {
	attrs := extra
	if pushNamespaces != nil {
		attrs += fmt.Sprintf("  push_namespaces = %s\n", hclStrings(pushNamespaces))
	}
	return testAccProviderConfig(host) + fmt.Sprintf(`
resource "kupe_registry_binding" "test" {
  cluster = %q
%s}
`, cluster, attrs)
}

func testAccCheckNoRegistryBindings(mock *mockKupeAPI) acc.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.registryBindings) != 0 {
			return fmt.Errorf("expected no cluster connections, got %d", len(mock.registryBindings))
		}
		return nil
	}
}

// testAccWantRegistryRequests checks the connection writes the mock received
// since the last check, as "METHOD cluster", in order.
func testAccWantRegistryRequests(mock *mockKupeAPI, seen *int, want ...string) acc.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		var got []string
		for _, req := range mock.registryRequests[*seen:] {
			if req.method == "GET" || !strings.Contains(req.path, "/bindings") {
				continue
			}
			got = append(got, req.method+" "+lastSegment(req.path))
		}
		*seen = len(mock.registryRequests)
		if !slices.Equal(got, want) {
			return fmt.Errorf("connection writes = %v, want %v", got, want)
		}
		return nil
	}
}

// lastBindingPatch is the body of the last connection PATCH.
func lastBindingWrite(mock *mockKupeAPI, method string) (mockRegistryRequest, bool) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for i := len(mock.registryRequests) - 1; i >= 0; i-- {
		req := mock.registryRequests[i]
		if req.method == method && strings.Contains(req.path, "/bindings") {
			return req, true
		}
	}
	return mockRegistryRequest{}, false
}

func TestAccRegistryBindingResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	seen := 0

	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryBindings(mock),
		Steps: []acc.TestStep{
			{
				// Pull only: no push namespaces.
				Config: testAccRegistryBindingConfig(mock.url(), "prod", nil, ""),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(bindingAddr, "registry", "registry"),
					acc.TestCheckResourceAttr(bindingAddr, "name", "prod"),
					acc.TestCheckResourceAttr(bindingAddr, "cluster", "prod"),
					acc.TestCheckResourceAttr(bindingAddr, "push_namespaces.#", "0"),
					acc.TestCheckResourceAttr(bindingAddr, "secret_name", "kupe-registry-push"),
					acc.TestCheckResourceAttr(bindingAddr, "phase", "Ready"),
					acc.TestCheckResourceAttr(bindingAddr, "pull_username", "kupe+acme+pull-prod"),
					acc.TestCheckResourceAttr(bindingAddr, "push_username", ""),
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.#", "0"),
					acc.TestMatchResourceAttr(bindingAddr, "message", regexp.MustCompile("restart workloads")),
					acc.TestCheckResourceAttrSet(bindingAddr, "etag"),
					testAccWantRegistryRequests(mock, &seen, "POST bindings"),
					func(*terraform.State) error {
						req, _ := lastBindingWrite(mock, "POST")
						if req.body["cluster"] != "prod" || req.body["secretName"] != "kupe-registry-push" {
							return fmt.Errorf("POST body = %v", req.body)
						}
						if _, sent := req.body["pushNamespaces"]; sent {
							return fmt.Errorf("POST body = %v, want no pushNamespaces", req.body)
						}
						return nil
					},
				),
			},
			{
				// Adding push namespaces is an in-place PATCH: never a DELETE
				// and POST, which would revoke the pull credential.
				Config:           testAccRegistryBindingConfig(mock.url(), "prod", ns("release", "ci"), ""),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionUpdate),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(bindingAddr, "push_namespaces.#", "2"),
					acc.TestCheckResourceAttr(bindingAddr, "push_username", "kupe+acme+push-prod"),
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.#", "2"),
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.0.name", "ci"),
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.0.phase", "Ready"),
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.1.name", "release"),
					testAccWantRegistryRequests(mock, &seen, "PATCH prod"),
					func(*terraform.State) error {
						req, _ := lastBindingWrite(mock, "PATCH")
						if fmt.Sprint(req.body["pushNamespaces"]) != "[ci release]" || req.ifMatch == "" {
							return fmt.Errorf("PATCH body %v, If-Match %q; want the sorted list and an If-Match",
								req.body, req.ifMatch)
						}
						return nil
					},
				),
			},
			{
				// Removing them all sends [].
				Config:           testAccRegistryBindingConfig(mock.url(), "prod", nil, ""),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionUpdate),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(bindingAddr, "push_namespaces.#", "0"),
					testAccWantRegistryRequests(mock, &seen, "PATCH prod"),
					func(*terraform.State) error {
						req, _ := lastBindingWrite(mock, "PATCH")
						if list, ok := req.body["pushNamespaces"].([]any); !ok || len(list) != 0 {
							return fmt.Errorf("PATCH body = %v, want pushNamespaces []", req.body)
						}
						return nil
					},
				),
			},
			{
				ResourceName:                         bindingAddr,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "registry/prod",
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// secret_name cannot change in place: DELETE, then POST, no PATCH.
				Config:           testAccRegistryBindingConfig(mock.url(), "prod", nil, "  secret_name = \"ci-push\"\n"),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionDestroyBeforeCreate),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(bindingAddr, "secret_name", "ci-push"),
					testAccWantRegistryRequests(mock, &seen, "DELETE prod", "POST bindings"),
				),
			},
			{
				// A different cluster replaces the connection.
				Config:           testAccRegistryBindingConfig(mock.url(), "staging", nil, "  secret_name = \"ci-push\"\n"),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionDestroyBeforeCreate),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(bindingAddr, "name", "staging"),
					testAccWantRegistryRequests(mock, &seen, "DELETE prod", "POST bindings"),
				),
			},
		},
	})
}

func expectBindingAction(action plancheck.ResourceActionType) acc.ConfigPlanChecks {
	return acc.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(bindingAddr, action)},
	}
}

func TestAccRegistryBindingResource_CreateWithPushNamespaces(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryBindings(mock),
		Steps: []acc.TestStep{{
			Config: testAccRegistryBindingConfig(mock.url(), "prod", ns("ci"), ""),
			Check: acc.ComposeAggregateTestCheckFunc(
				acc.TestCheckResourceAttr(bindingAddr, "name", "prod"),
				acc.TestCheckResourceAttr(bindingAddr, "push_namespaces.#", "1"),
				acc.TestCheckResourceAttr(bindingAddr, "namespace_status.0.name", "ci"),
				func(*terraform.State) error {
					req, _ := lastBindingWrite(mock, "POST")
					if fmt.Sprint(req.body["pushNamespaces"]) != "[ci]" {
						return fmt.Errorf("POST body = %v, want pushNamespaces [ci]", req.body)
					}
					return nil
				},
			),
		}},
	})
}

// TestAccRegistryBindingResource_UpdateWaitsForNewNamespace: right after the
// PATCH the platform still reports the old list as Ready; Update waits until
// the namespace added is delivered, so state ends with its status.
func TestAccRegistryBindingResource_UpdateWaitsForNewNamespace(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryBindings(mock),
		Steps: []acc.TestStep{
			{Config: testAccRegistryBindingConfig(mock.url(), "prod", ns("ci"), "")},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.registryBindingLagOnPatch = 3 }) },
				Config:           testAccRegistryBindingConfig(mock.url(), "prod", ns("ci", "release"), ""),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionUpdate),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.#", "2"),
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.1.name", "release"),
					acc.TestCheckResourceAttr(bindingAddr, "namespace_status.1.phase", "Ready"),
				),
			},
		},
	})
}

func TestAccRegistryBindingResource_PlanTimeErrors(t *testing.T) {
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("ns-%d", i)
	}
	tests := []struct {
		name, cluster, extra string
		push                 []string
		wantErr              string
	}{
		{name: "11 push namespaces", cluster: "prod", push: eleven, wantErr: `at most 10`},
		{name: "kube-system", cluster: "prod", push: ns("kube-system"), wantErr: `Reserved namespace`},
		{name: "argocd", cluster: "prod", push: ns("argocd"), wantErr: `Reserved namespace`},
		{name: "not a DNS label", cluster: "prod", push: ns("CI"), wantErr: `must be lowercase`},
		{name: "the pull Secret's name", cluster: "prod", extra: "  secret_name = \"kupe-registry-pull\"\n", wantErr: `kupe-registry-pull`},
		{name: "another registry", cluster: "prod", extra: "  registry = \"images\"\n", wantErr: `a tenant has one registry, named\s+registry`},
		{name: "-- in the cluster", cluster: "prod--eu", wantErr: `not contain '--'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedRegistry(mock, "")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []acc.TestStep{{
					Config:      testAccRegistryBindingConfig(mock.url(), tt.cluster, tt.push, tt.extra),
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
			if len(mock.registryRequests) != 0 {
				t.Errorf("the plan reached the API: %d registry requests", len(mock.registryRequests))
			}
		})
	}
}

func TestAccRegistryBindingResource_ImportRejectsOtherIDs(t *testing.T) {
	for _, id := range []string{"prod", "images/prod", "registry/", "registry/a/b"} {
		t.Run(id, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedRegistry(mock, "")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoRegistryBindings(mock),
				Steps: []acc.TestStep{
					{Config: testAccRegistryBindingConfig(mock.url(), "prod", nil, "")},
					{
						ResourceName:  bindingAddr,
						ImportState:   true,
						ImportStateId: id,
						ExpectError:   regexp.MustCompile(`expected "registry/<cluster>"`),
					},
				},
			})
		})
	}
}

// TestAccRegistryBindingResource_AlreadyConnected: a cluster that already
// has a connection gets the import command with its own name.
func TestAccRegistryBindingResource_AlreadyConnected(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	mock.setState(func(m *mockKupeAPI) {
		m.registryBindings["prod"] = map[string]any{
			"name": "prod", "registry": "registry", "cluster": "prod", "pushNamespaces": []string{},
			"secretName": "", "resourceVersion": "1", "createdAt": "2026-10-21T10:05:00Z",
		}
	})
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []acc.TestStep{{
			Config: testAccRegistryBindingConfig(mock.url(), "prod", nil, ""),
			ExpectError: regexp.MustCompile(`(?s)Error code: REGISTRY_BINDING_EXISTS.*` +
				wrappedText("`terraform import kupe_registry_binding.this registry/prod`")),
		}},
	})
}

// TestAccRegistryBindingResource_NoRegistryYet (F-2): a connection created
// before the registry exists gets the hint to reference kupe_registry.this.
func TestAccRegistryBindingResource_NoRegistryYet(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []acc.TestStep{{
			Config:      testAccRegistryBindingConfig(mock.url(), "prod", nil, ""),
			ExpectError: regexp.MustCompile(`(?s)failed to create .*no\s+registry\s+yet.*kupe_registry\.this\.name`),
		}},
	})
}

// TestAccRegistryBindingResource_FailedNamespace: a push namespace the
// platform cannot deliver into ends Create at once; the connection stays in
// state, tainted, and the next apply replaces it.
func TestAccRegistryBindingResource_FailedNamespace(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	mock.setState(func(m *mockKupeAPI) {
		m.registryBindingStatus = map[string]any{
			"phase":   "Failed",
			"message": `namespace ci: a Secret "kupe-registry-push" that the platform did not create exists`,
			"namespaces": []any{map[string]any{
				"name": "ci", "phase": "Failed", "message": "a Secret of the same name exists",
			}},
		}
	})
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryBindings(mock),
		Steps: []acc.TestStep{
			{
				Config: testAccRegistryBindingConfig(mock.url(), "prod", ns("ci"), ""),
				ExpectError: regexp.MustCompile(`(?s)cluster connection failed.*namespace\s+"ci".*` +
					`another\s+Secret\s+named\s+"kupe-registry-push".*marked\s+tainted`),
			},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.registryBindingStatus = nil }) },
				Config:           testAccRegistryBindingConfig(mock.url(), "prod", ns("ci"), ""),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionReplace),
			},
		},
	})
}

// TestAccRegistryBindingResource_RemovedFailedNamespace (F-6): a namespace
// taken out of push_namespaces whose status is still Failed does not fail
// the Update: only the listed namespaces count. The update also adds tools,
// which the platform delivers a poll later, so the wait sees ci Failed
// before the new list is Ready.
func TestAccRegistryBindingResource_RemovedFailedNamespace(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	ciFailed := map[string]any{"name": "ci", "phase": "Failed", "message": "a Secret of the same name exists"}
	ready := func(name string) map[string]any { return map[string]any{"name": name, "phase": "Ready", "message": ""} }
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryBindings(mock),
		Steps: []acc.TestStep{
			{Config: testAccRegistryBindingConfig(mock.url(), "prod", ns("ci", "release"), "")},
			{
				// After the PATCH the platform still reports ci Failed while
				// it withdraws it, and delivers tools on the second poll.
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.registryBindingPatchStatuses = []map[string]any{
							{"namespaces": []any{ciFailed, ready("release")}},
							{"namespaces": []any{ciFailed, ready("release"), ready("tools")}},
						}
					})
				},
				Config:           testAccRegistryBindingConfig(mock.url(), "prod", ns("release", "tools"), ""),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionUpdate),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(bindingAddr, "phase", "Ready"),
					acc.TestCheckResourceAttr(bindingAddr, "push_namespaces.#", "2"),
					acc.TestCheckTypeSetElemAttr(bindingAddr, "push_namespaces.*", "release"),
					acc.TestCheckTypeSetElemAttr(bindingAddr, "push_namespaces.*", "tools"),
					func(*terraform.State) error {
						req, _ := lastBindingWrite(mock, "PATCH")
						if fmt.Sprint(req.body["pushNamespaces"]) != "[release tools]" {
							return fmt.Errorf("PATCH body = %v, want pushNamespaces [release tools]", req.body)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccRegistryBindingResource_RegistriesUnavailable: with the registry
// switched off, adding a push namespace is refused with the hint, taking
// one out is served, and the connection can be destroyed.
func TestAccRegistryBindingResource_RegistriesUnavailable(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryBindings(mock),
		Steps: []acc.TestStep{
			{Config: testAccRegistryBindingConfig(mock.url(), "prod", ns("ci", "release"), "")},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.registriesDisabled = true }) },
				Config:    testAccRegistryBindingConfig(mock.url(), "prod", ns("ci", "release", "tools"), ""),
				ExpectError: regexp.MustCompile(`(?s)failed to update cluster connection.*` +
					wrappedText("Registries are not enabled")),
			},
			{
				Config:           testAccRegistryBindingConfig(mock.url(), "prod", ns("ci"), ""),
				ConfigPlanChecks: expectBindingAction(plancheck.ResourceActionUpdate),
				Check:            acc.TestCheckResourceAttr(bindingAddr, "push_namespaces.#", "1"),
			},
			{
				Config: testAccProviderConfig(mock.url()),
				Check:  testAccCheckNoRegistryBindings(mock),
			},
		},
	})
}

// --- Direct calls, for the diagnostics the test harness does not show ---

// testResourceSchema is r's schema.
func testResourceSchema(t *testing.T, r resource.Resource) resource.SchemaResponse {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp
}

// testPlanValue is a raw value for s with attrs set and every other
// attribute null, or unknown when it is computed.
func testPlanValue(t *testing.T, s resource.SchemaResponse, attrs map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	objType := s.Schema.Type().TerraformType(ctx).(tftypes.Object)
	values := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if v, ok := attrs[name]; ok {
			values[name] = v
			continue
		}
		if a := s.Schema.Attributes[name]; a.IsComputed() && name != "timeouts" {
			values[name] = tftypes.NewValue(typ, tftypes.UnknownValue)
		} else {
			values[name] = tftypes.NewValue(typ, nil)
		}
	}
	return tftypes.NewValue(objType, values)
}

func warnings(diags diag.Diagnostics) []string {
	var out []string
	for _, d := range diags.Warnings() {
		out = append(out, d.Summary()+": "+d.Detail())
	}
	return out
}

// TestRegistryBindingCreate_RestartWarningOnCreateOnly: Create warns once
// that pods started earlier need a restart; a refresh (Read) does not.
func TestRegistryBindingCreate_RestartWarningOnCreateOnly(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	ctx := context.Background()
	r := NewRegistryBindingResource().(*RegistryBindingResource)
	r.client = client.New(mock.url(), "acme", "kupe_test_key")
	s := testResourceSchema(t, r)

	raw := testPlanValue(t, s, map[string]tftypes.Value{
		"registry":        tftypes.NewValue(tftypes.String, "registry"),
		"cluster":         tftypes.NewValue(tftypes.String, "prod"),
		"secret_name":     tftypes.NewValue(tftypes.String, "kupe-registry-push"),
		"push_namespaces": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{}),
	})
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: raw.Copy()}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s.Schema, Raw: raw}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", createResp.Diagnostics)
	}
	got := warnings(createResp.Diagnostics)
	if len(got) != 1 || !strings.Contains(got[0], registryBindingRestartNotice) {
		t.Fatalf("Create warnings = %q, want one with %q", got, registryBindingRestartNotice)
	}

	readResp := resource.ReadResponse{State: createResp.State}
	r.Read(ctx, resource.ReadRequest{State: createResp.State}, &readResp)
	if readResp.Diagnostics.HasError() || len(readResp.Diagnostics.Warnings()) != 0 {
		t.Errorf("Read diagnostics = %v, want none", readResp.Diagnostics)
	}
}
