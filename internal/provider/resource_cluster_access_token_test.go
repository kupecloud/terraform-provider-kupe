package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const catAddr = "kupe_cluster_access_token.test"

func testAccClusterAccessTokenConfig(host, cluster, displayName, role, extra string) string {
	return testAccProviderConfig(host) + fmt.Sprintf(`
resource "kupe_cluster_access_token" "test" {
  cluster      = %q
  display_name = %q
  access = {
    role = %q
  }
%s}
`, cluster, displayName, role, extra)
}

// seedCATClusters adds clusters the access token routes find.
func seedCATClusters(mock *mockKupeAPI, names ...string) {
	mock.setState(func(m *mockKupeAPI) {
		for _, name := range names {
			m.clusters[name] = map[string]any{
				"name": name, "displayName": name, "type": "shared", "version": "1.35",
				"generation": 1, "resourceVersion": "1", "createdAt": "2026-11-01T00:00:00Z",
				"status": map[string]any{"phase": "Running", "observedGeneration": 1},
			}
		}
	})
}

func testAccCheckNoClusterAccessTokens(mock *mockKupeAPI) acc.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.catTokens) != 0 {
			return fmt.Errorf("expected no cluster access tokens, got %d", len(mock.catTokens))
		}
		return nil
	}
}

// catID is the id of the one access token in the mock.
func catID(mock *mockKupeAPI) string {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for id := range mock.catTokens {
		return id
	}
	return ""
}

func expectCATAction(action plancheck.ResourceActionType) acc.ConfigPlanChecks {
	return acc.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(catAddr, action)},
	}
}

func TestAccClusterAccessTokenResource(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "GitHub Actions", "admin", "  expires_in_days = 30\n")
	var id string

	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{
				Config: config,
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(catAddr, plancheck.ResourceActionCreate),
						plancheck.ExpectSensitiveValue(catAddr, tfjsonpath.New("token")),
						plancheck.ExpectSensitiveValue(catAddr, tfjsonpath.New("kubeconfig")),
					},
				},
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestMatchResourceAttr(catAddr, "id", validClusterAccessTokenID),
					acc.TestCheckResourceAttr(catAddr, "cluster", "dev"),
					acc.TestCheckResourceAttr(catAddr, "access.role", "admin"),
					acc.TestCheckResourceAttr(catAddr, "expires_in_days", "30"),
					acc.TestCheckResourceAttr(catAddr, "created_at", "2026-11-22T10:00:00Z"),
					acc.TestCheckResourceAttr(catAddr, "expires_at", "2026-12-22T10:00:00Z"),
					acc.TestCheckResourceAttr(catAddr, "created_by", "admin@acme.com"),
					acc.TestCheckResourceAttr(catAddr, "phase", "Ready"),
					acc.TestCheckResourceAttr(catAddr, "collected", "true"),
					func(s *terraform.State) error {
						attrs := s.RootModule().Resources[catAddr].Primary.Attributes
						id = attrs["id"]
						if attrs["token"] != mockClusterAccessToken(id) {
							return fmt.Errorf("token in state has length %d, want the collected token", len(attrs["token"]))
						}
						if attrs["kubeconfig"] != mockClusterAccessKubeconfig("dev", id) {
							return fmt.Errorf("kubeconfig in state has length %d, want the collected kubeconfig", len(attrs["kubeconfig"]))
						}
						mock.mu.Lock()
						defer mock.mu.Unlock()
						posts := mock.mockCATRequestsOf("POST", mockClustersPath+"/dev/access-tokens")
						if len(posts) != 1 {
							return fmt.Errorf("create POSTs = %d, want 1", len(posts))
						}
						body := posts[0].body
						access, _ := body["access"].(map[string]any)
						if body["expiresInDays"] != float64(30) || body["displayName"] != "GitHub Actions" || access["role"] != "admin" {
							return fmt.Errorf("POST body = %v", body)
						}
						if n := len(mock.mockCATRequestsOf("POST", mockClustersPath+"/dev/access-tokens/"+id+"/token")); n != 1 {
							return fmt.Errorf("collects = %d, want 1", n)
						}
						return nil
					},
				),
			},
			{
				// A refresh keeps the token and kubeconfig, never collects
				// again and plans nothing.
				Config: config,
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(s *terraform.State) error {
					attrs := s.RootModule().Resources[catAddr].Primary.Attributes
					if attrs["token"] != mockClusterAccessToken(id) || attrs["kubeconfig"] == "" {
						return fmt.Errorf("token (length %d) or kubeconfig (length %d) changed on refresh",
							len(attrs["token"]), len(attrs["kubeconfig"]))
					}
					mock.mu.Lock()
					defer mock.mu.Unlock()
					if n := len(mock.mockCATRequestsOf("POST", mockClustersPath+"/dev/access-tokens/"+id+"/token")); n != 1 {
						return fmt.Errorf("collects = %d after a refresh, want 1", n)
					}
					return nil
				},
			},
			{
				// A timeout change is recorded in place, not a new token.
				Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "GitHub Actions", "admin",
					"  expires_in_days = 30\n  timeouts = { create = \"10m\" }\n"),
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionUpdate),
			},
			{
				// The token cannot be read back: null after import.
				ResourceName:            catAddr,
				ImportState:             true,
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return "dev/" + catID(mock), nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token", "kubeconfig", "timeouts"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances", len(states))
					}
					attrs := states[0].Attributes
					for _, name := range []string{"token", "kubeconfig"} {
						if v, ok := attrs[name]; ok && v != "" {
							return fmt.Errorf("%s after import has length %d, want null", name, len(v))
						}
					}
					if attrs["expires_in_days"] != "30" || attrs["access.role"] != "admin" {
						return fmt.Errorf("after import expires_in_days = %q, access.role = %q", attrs["expires_in_days"], attrs["access.role"])
					}
					return nil
				},
			},
			{
				// Revoked outside Terraform: a refresh removes it and the
				// plan creates a new one.
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.catTokens = map[string]map[string]any{} })
				},
				Config:           config,
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionCreate),
				Check: func(s *terraform.State) error {
					if got := s.RootModule().Resources[catAddr].Primary.Attributes["id"]; got == id {
						return fmt.Errorf("the new token kept the old id %s", id)
					}
					return nil
				},
			},
		},
	})
}

// TestAccClusterAccessTokenResource_ImportThenEmptyPlan: an imported token
// has a null token and kubeconfig, and the plan after the import is empty.
func TestAccClusterAccessTokenResource_ImportThenEmptyPlan(t *testing.T) {
	const id = "cat-00000000000000aa"
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) {
		m.catTokens[id] = map[string]any{
			"id": id, "cluster": "dev", "displayName": "ci", "access": map[string]any{"role": "admin"},
			"createdBy": "admin@acme.com", "createdAt": "2026-11-22T10:00:00Z", "expiresAt": "2027-02-20T10:00:00Z",
			"collectedAt": "2026-11-22T10:00:05Z", "collected": true,
			"status": map[string]any{"phase": "Ready", "message": "", "conditions": []any{}},
		}
	})
	// The persisted import reuses the working directory the first step
	// initialised, with the provider's source named (as the secret values
	// import test does).
	config := `terraform {
  required_providers {
    kupe = { source = "kupecloud/kupe" }
  }
}
` + testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{Config: testAccProviderConfig(mock.url())},
			{
				Config:             config,
				ResourceName:       catAddr,
				ImportState:        true,
				ImportStatePersist: true,
				ImportStateId:      "dev/" + id,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					attrs := states[0].Attributes
					for _, name := range []string{"token", "kubeconfig"} {
						if v, ok := attrs[name]; ok && v != "" {
							return fmt.Errorf("%s after import has length %d, want null", name, len(v))
						}
					}
					if attrs["expires_in_days"] != "90" || attrs["collected"] != "true" || attrs["access.role"] != "admin" {
						return fmt.Errorf("imported attributes: expires_in_days %q collected %q access.role %q",
							attrs["expires_in_days"], attrs["collected"], attrs["access.role"])
					}
					return nil
				},
			},
			{
				Config: config,
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: acc.TestCheckNoResourceAttr(catAddr, "token"),
			},
		},
	})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if n := len(mock.mockCATRequestsOf("POST", mockClustersPath+"/dev/access-tokens/"+id+"/token")); n != 0 {
		t.Errorf("import collected the token %d times, want never", n)
	}
}

// TestAccClusterAccessTokenResource_DefaultLifetime: without expires_in_days
// the request omits it and state records the platform's 90 days.
func TestAccClusterAccessTokenResource_DefaultLifetime(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{
				Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", ""),
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(catAddr, "expires_in_days", "90"),
					acc.TestCheckResourceAttr(catAddr, "expires_at", "2027-02-20T10:00:00Z"),
					func(*terraform.State) error {
						mock.mu.Lock()
						defer mock.mu.Unlock()
						posts := mock.mockCATRequestsOf("POST", mockClustersPath+"/dev/access-tokens")
						if _, sent := posts[0].body["expiresInDays"]; sent {
							return fmt.Errorf("POST body = %v, want no expiresInDays", posts[0].body)
						}
						return nil
					},
				),
			},
			{
				// Naming the default explicitly changes nothing.
				Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "  expires_in_days = 90\n"),
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccClusterAccessTokenResource_InputChangesReplace: every input
// replaces the token.
func TestAccClusterAccessTokenResource_InputChangesReplace(t *testing.T) {
	tests := []struct {
		name, cluster, displayName, extra string
	}{
		{name: "display_name", cluster: "dev", displayName: "renamed"},
		{name: "cluster", cluster: "prod", displayName: "ci"},
		{name: "expires_in_days", cluster: "dev", displayName: "ci", extra: "  expires_in_days = 7\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedCATClusters(mock, "dev", "prod")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
				Steps: []acc.TestStep{
					{Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")},
					{
						Config:           testAccClusterAccessTokenConfig(mock.url(), tt.cluster, tt.displayName, "admin", tt.extra),
						ConfigPlanChecks: expectCATAction(plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			})
		})
	}
}

// TestAccClusterAccessTokenResource_RoleChangeReplaces: v1 offers one role,
// so the configuration cannot change it; a token whose role differs from the
// configuration (as one created elsewhere with another role would) is
// replaced.
func TestAccClusterAccessTokenResource_RoleChangeReplaces(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					id := catID(mock)
					mock.setState(func(m *mockKupeAPI) { m.catTokens[id]["access"] = map[string]any{"role": "readonly"} })
				},
				Config:           config,
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionDestroyBeforeCreate),
			},
		},
	})
}

func TestAccClusterAccessTokenResource_PlanTimeErrors(t *testing.T) {
	tests := []struct {
		name, cluster, displayName, role, extra, wantErr string
	}{
		{name: "readonly role", cluster: "dev", displayName: "ci", role: "readonly", wantErr: `value must be one of`},
		{name: "lifetime 0", cluster: "dev", displayName: "ci", role: "admin", extra: "  expires_in_days = 0\n", wantErr: `between 1 and 365`},
		{name: "lifetime 366", cluster: "dev", displayName: "ci", role: "admin", extra: "  expires_in_days = 366\n", wantErr: `between 1 and 365`},
		{name: "empty name", cluster: "dev", displayName: "", role: "admin", wantErr: `character count must be between 1 and 128`},
		{name: "129 characters", cluster: "dev", displayName: strings.Repeat("é", 129), role: "admin", wantErr: `character count must be between 1 and 128`},
		{name: "newline", cluster: "dev", displayName: "ci\nbuild", role: "admin", wantErr: `must be printable text,\s+without control\s+characters`},
		{name: "cluster name", cluster: "Dev_1", displayName: "ci", role: "admin", wantErr: `must be lowercase letters`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedCATClusters(mock, "dev")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []acc.TestStep{{
					Config:      testAccClusterAccessTokenConfig(mock.url(), tt.cluster, tt.displayName, tt.role, tt.extra),
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
			if len(mock.catRequests) != 0 {
				t.Errorf("the plan reached the API: %d access token requests", len(mock.catRequests))
			}
		})
	}
}

// TestAccClusterAccessTokenResource_FailedOnCreate: a token the platform
// cannot issue fails the apply; it stays in state, tainted, and is replaced
// next.
func TestAccClusterAccessTokenResource_FailedOnCreate(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) { m.catPhases = []string{"Pending", "Failed"} })
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)cluster access token failed.*can never\s+work.*Status message: the access\s+token\s+could\s+not\s+be\s+issued.*marked\s+tainted`),
			},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.catPhases = nil }) },
				Config:           config,
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionReplace),
				Check:            acc.TestCheckResourceAttr(catAddr, "collected", "true"),
			},
		},
	})
}

// TestAccClusterAccessTokenResource_Timeout: a token never issued fails at
// the create timeout with the hint; it is tainted and replaced next.
func TestAccClusterAccessTokenResource_Timeout(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) { m.catPhases = []string{"Pending"} })
	timeoutsExtra := "  timeouts = { create = \"1s\" }\n"
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", timeoutsExtra)
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)timed out while Pending.*timeouts.create\s+=\s+"4m".*marked\s+tainted`),
			},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.catPhases = nil }) },
				Config:           config,
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionReplace),
			},
		},
	})
}

// TestAccClusterAccessTokenResource_CollectNotReadyRetried: 409
// ACCESS_TOKEN_NOT_READY (also a claim conflict) is tried again within the
// timeout until the collect is served.
func TestAccClusterAccessTokenResource_CollectNotReadyRetried(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	notReady := mockCollectAnswer{status: http.StatusConflict, code: client.CodeAccessTokenNotReady}
	mock.setState(func(m *mockKupeAPI) {
		m.catPhases = []string{"Pending", "Ready"}
		m.catCollectAnswers = []mockCollectAnswer{notReady, notReady}
	})
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{{
			Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", ""),
			Check: acc.ComposeAggregateTestCheckFunc(
				acc.TestCheckResourceAttr(catAddr, "collected", "true"),
				func(s *terraform.State) error {
					id := s.RootModule().Resources[catAddr].Primary.Attributes["id"]
					if s.RootModule().Resources[catAddr].Primary.Attributes["token"] != mockClusterAccessToken(id) {
						return fmt.Errorf("no token in state")
					}
					mock.mu.Lock()
					defer mock.mu.Unlock()
					if n := len(mock.mockCATRequestsOf("POST", mockClustersPath+"/dev/access-tokens/"+id+"/token")); n != 3 {
						return fmt.Errorf("collects = %d, want 3 (two refused not ready, then served)", n)
					}
					return nil
				},
			),
		}},
	})
}

// TestAccClusterAccessTokenResource_CollectRefused: a collect refused for
// good ends Create with the API's answer; the token is tainted and the next
// apply replaces it.
func TestAccClusterAccessTokenResource_CollectRefused(t *testing.T) {
	tests := []struct {
		name    string
		answer  mockCollectAnswer
		wantErr string
	}{
		{
			name:    "already collected",
			answer:  mockCollectAnswer{status: http.StatusGone, code: client.CodeAccessTokenAlreadyCollected},
			wantErr: `(?s)already collected.*access token "cat-[a-f0-9]{16}" was\s+collected\s+outside\s+this\s+apply,\s+so\s+Terraform\s+cannot\s+hold\s+it;\s+it\s+is\s+tainted\s+and\s+the\s+next\s+apply\s+replaces\s+it\.`,
		},
		{
			name:    "failed",
			answer:  mockCollectAnswer{status: http.StatusConflict, code: client.CodeAccessTokenFailed},
			wantErr: `(?s)could not be collected.*409.*Error code: ACCESS_TOKEN_FAILED.*marked\s+tainted`,
		},
		{
			name:    "creator only",
			answer:  mockCollectAnswer{status: http.StatusForbidden, code: client.CodeAccessTokenCollectCreatorOnly},
			wantErr: `(?s)could not be collected.*403.*Error code: ACCESS_TOKEN_COLLECT_CREATOR_ONLY.*marked\s+tainted`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shrinkPollInterval(t)
			mock := newMockKupeAPI()
			defer mock.close()
			seedCATClusters(mock, "dev")
			mock.setState(func(m *mockKupeAPI) { m.catCollectAnswers = []mockCollectAnswer{tt.answer} })
			config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
				Steps: []acc.TestStep{
					{Config: config, ExpectError: regexp.MustCompile(tt.wantErr)},
					{
						Config:           config,
						ConfigPlanChecks: expectCATAction(plancheck.ResourceActionReplace),
						Check:            acc.TestCheckResourceAttr(catAddr, "collected", "true"),
					},
				},
			})
		})
	}
}

// TestAccClusterAccessTokenResource_CollectedElsewhere: a Ready token the
// API reports as already collected is not collected again: tainted.
func TestAccClusterAccessTokenResource_CollectedElsewhere(t *testing.T) {
	shrinkPollInterval(t)
	// The first GET (Pending) is followed by a collect elsewhere, before the
	// GET that reports the token Ready.
	var mock *mockKupeAPI
	mock = newMockKupeAPI(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if r.Method == http.MethodGet && isMockClusterAccessTokenPath(r.URL.Path) {
				mock.setState(func(m *mockKupeAPI) {
					for _, tok := range m.catTokens {
						tok["collected"], tok["collectedAt"] = true, "2026-11-22T10:00:01Z"
					}
				})
			}
		})
	})
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) { m.catPhases = []string{"Pending", "Ready"} })
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{{
			Config:      testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", ""),
			ExpectError: regexp.MustCompile(`(?s)already collected.*was\s+collected\s+outside\s+this\s+apply`),
		}},
	})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, req := range mock.catRequests {
		if strings.HasSuffix(req.path, "/token") {
			t.Errorf("collected a token the API reports as collected: %s %s", req.method, req.path)
		}
	}
}

// TestAccClusterAccessTokenResource_RefusedCollectIsNotLost: a collect the
// API refuses (409 not ready) was never delivered, so a token reported
// collected afterwards was collected elsewhere, not lost by this apply.
func TestAccClusterAccessTokenResource_RefusedCollectIsNotLost(t *testing.T) {
	shrinkPollInterval(t)
	var mock *mockKupeAPI
	mock = newMockKupeAPI(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/token") {
				mock.setState(func(m *mockKupeAPI) {
					for _, tok := range m.catTokens {
						tok["collected"], tok["collectedAt"] = true, "2026-11-22T10:00:01Z"
					}
				})
			}
		})
	})
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) {
		m.catCollectAnswers = []mockCollectAnswer{{status: http.StatusConflict, code: client.CodeAccessTokenNotReady}}
	})
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{{
			Config:      testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", ""),
			ExpectError: regexp.MustCompile(`(?s)already collected.*was\s+collected\s+outside\s+this\s+apply`),
		}},
	})
}

// TestAccClusterAccessTokenResource_RevokedDuringCollect: a token gone (404)
// at the collect is removed from state, with nothing left to revoke; the
// next apply creates a new one.
func TestAccClusterAccessTokenResource_RevokedDuringCollect(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) { m.catRevokeOnCollect = true })
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)removed before it was collected.*nothing\s+to\s+revoke`),
			},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.catRevokeOnCollect = false }) },
				Config:           config,
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionCreate),
			},
		},
	})
}

func TestAccClusterAccessTokenResource_LimitReachedHint(t *testing.T) {
	mock := newMockKupeAPI(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == mockClustersPath+"/dev/access-tokens" {
				mockError(w, http.StatusConflict, client.CodeAccessTokenLimitReached,
					"the cluster already has 20 access tokens; revoke one first")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	defer mock.close()
	seedCATClusters(mock, "dev")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []acc.TestStep{{
			Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", ""),
			ExpectError: regexp.MustCompile(`(?s)failed to create cluster access token.*Error code: ACCESS_TOKEN_LIMIT_REACHED.*` +
				wrappedText("destroy its `kupe_cluster_access_token`")),
		}},
	})
}

// TestAccClusterAccessTokenResource_ImportRejectsOtherIDs checks the import
// ID.
func TestAccClusterAccessTokenResource_ImportRejectsOtherIDs(t *testing.T) {
	for _, id := range []string{
		"cat-0a1b2c3d4e5f6a7b", "dev/rt-0a1b2c3d4e5f6a7b", "dev/cat-0A1B2C3D4E5F6A7B", "Dev/cat-0a1b2c3d4e5f6a7b",
		"dev/cat-0a1b2c3d4e5f6a7b/x", "/cat-0a1b2c3d4e5f6a7b",
	} {
		t.Run(id, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedCATClusters(mock, "dev")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
				Steps: []acc.TestStep{
					{Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")},
					{
						ResourceName:  catAddr,
						ImportState:   true,
						ImportStateId: id,
						ExpectError:   regexp.MustCompile(`expected "<cluster>/<id>"`),
					},
				},
			})
		})
	}
}

// TestAccClusterAccessTokenResource_DestroyAlreadyRevoked: a DELETE that
// answers 404 (revoked after the refresh) is a successful destroy.
func TestAccClusterAccessTokenResource_DestroyAlreadyRevoked(t *testing.T) {
	var revoked atomic.Bool
	mock := newMockKupeAPI(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if revoked.Load() && r.Method == http.MethodDelete && isMockClusterAccessTokenPath(r.URL.Path) {
				mockNotFound(w, "access token", lastSegment(r.URL.Path))
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	defer mock.close()
	seedCATClusters(mock, "dev")
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []acc.TestStep{
			{Config: config},
			{PreConfig: func() { revoked.Store(true) }, Config: config, Destroy: true},
		},
	})
}

// TestAccClusterAccessTokenResource_TokenNotLogged: provider logs at TRACE
// carry the token's id but never the token or kubeconfig.
func TestAccClusterAccessTokenResource_TokenNotLogged(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "acc.log")
	t.Setenv("TF_LOG", "TRACE")
	t.Setenv("TF_ACC_LOG_PATH", logPath)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	var id, token, kubeconfig string
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{{
			Config: testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", ""),
			Check: func(s *terraform.State) error {
				attrs := s.RootModule().Resources[catAddr].Primary.Attributes
				id, token, kubeconfig = attrs["id"], attrs["token"], attrs["kubeconfig"]
				return nil
			},
		}},
	})
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || kubeconfig == "" {
		t.Fatalf("no token (length %d) or kubeconfig (length %d) in state", len(token), len(kubeconfig))
	}
	// Control: the provider's own collect log line was captured.
	if !strings.Contains(string(logs), "collected cluster access token") || !strings.Contains(string(logs), id) {
		t.Fatalf("the provider's collect log line with id %s is not in the captured logs (%d bytes)", id, len(logs))
	}
	// The token's distinctive middle part is in the kubeconfig too.
	for name, secret := range map[string]string{"token": token, "token part": "mock-" + strings.TrimPrefix(id, "cat-")} {
		if strings.Contains(string(logs), secret) {
			t.Errorf("the %s (length %d) appears in the captured logs", name, len(secret))
		}
	}
}

// TestClusterAccessTokenSchema_Sensitive pins the token and kubeconfig:
// computed, Sensitive, described verbatim in the docs and the index.
func TestClusterAccessTokenSchema_Sensitive(t *testing.T) {
	s := testResourceSchema(t, NewClusterAccessTokenResource())
	for _, name := range []string{"token", "kubeconfig"} {
		attr, ok := s.Schema.Attributes[name].(schema.StringAttribute)
		if !ok || !attr.Sensitive || !attr.Computed || attr.Optional || attr.Required {
			t.Fatalf("%s attribute = %+v, want computed and Sensitive", name, s.Schema.Attributes[name])
		}
		if !strings.Contains(attr.Description, "stored in Terraform state in plain text") {
			t.Errorf("%s description %q does not say it is stored in state in plain text", name, attr.Description)
		}
	}
	doc, err := os.ReadFile("../../docs/resources/cluster_access_token.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), clusterAccessTokenSecretDescription) {
		t.Error("docs/resources/cluster_access_token.md does not state the token description verbatim; run make docs")
	}
	index, err := os.ReadFile("../../docs/index.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "`kupe_cluster_access_token.token`") ||
		!strings.Contains(string(index), "`kupe_cluster_access_token.kubeconfig`") {
		t.Error("docs/index.md does not name kupe_cluster_access_token.token and .kubeconfig under the secrets in state")
	}
}

// testCATPlan is a create plan for a cluster access token on dev.
func testCATPlan(t *testing.T, s resource.SchemaResponse) tftypes.Value {
	t.Helper()
	accessType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"role": tftypes.String}}
	return testPlanValue(t, s, map[string]tftypes.Value{
		"cluster":      tftypes.NewValue(tftypes.String, "dev"),
		"display_name": tftypes.NewValue(tftypes.String, "ci"),
		"access": tftypes.NewValue(accessType, map[string]tftypes.Value{
			"role": tftypes.NewValue(tftypes.String, "admin"),
		}),
	})
}

// TestClusterAccessTokenCreate_InterruptedTracksToken: an apply interrupted
// after the POST, while it waits for Ready, keeps the token in state, and
// the destroy revokes it.
func TestClusterAccessTokenCreate_InterruptedTracksToken(t *testing.T) {
	shrinkPollInterval(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mock := newMockKupeAPI(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The apply stops once the POST has been answered: at the first
			// GET of the wait.
			if r.Method == http.MethodGet && isMockClusterAccessTokenPath(r.URL.Path) {
				cancel()
			}
			next.ServeHTTP(w, r)
		})
	})
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) { m.catPhases = []string{"Pending"} })

	r := NewClusterAccessTokenResource().(*ClusterAccessTokenResource)
	r.client = client.New(mock.url(), "acme", "kupe_test_key")
	s := testResourceSchema(t, r)
	plan := tfsdk.Plan{Schema: s.Schema, Raw: testCATPlan(t, s)}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: plan.Raw.Copy()}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

	if errs := resp.Diagnostics.Errors(); len(errs) != 1 || errs[0].Summary() != "cluster access token create interrupted" {
		t.Fatalf("diagnostics = %v, want the one interrupted error", resp.Diagnostics)
	}
	var state ClusterAccessTokenResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("state: %v", diags)
	}
	id := catID(mock)
	if id == "" || state.ID.ValueString() != id || !state.Token.IsNull() {
		t.Fatalf("state after the interrupt: id %q (mock %q), token null %v", state.ID.ValueString(), id, state.Token.IsNull())
	}

	var delResp resource.DeleteResponse
	r.Delete(context.Background(), resource.DeleteRequest{State: resp.State}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("Delete: %v", delResp.Diagnostics)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.catTokens) != 0 || len(mock.mockCATRequestsOf("DELETE", mockClustersPath+"/dev/access-tokens/"+id)) != 1 {
		t.Errorf("the destroy did not revoke %s (%d tokens left)", id, len(mock.catTokens))
	}
}

// TestClusterAccessTokenRead_ExpiredWarns: a token past its expiry that the
// platform has not removed yet warns on refresh and stays in state.
func TestClusterAccessTokenRead_ExpiredWarns(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	ctx := context.Background()
	c := client.New(mock.url(), "acme", "kupe_test_key")
	created, err := c.CreateClusterAccessToken(ctx, "dev", client.CreateClusterAccessTokenRequest{
		DisplayName: "ci", Access: client.ClusterAccess{Role: "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := NewClusterAccessTokenResource().(*ClusterAccessTokenResource)
	r.client = c
	s := testResourceSchema(t, r)
	accessType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"role": tftypes.String}}
	raw := testPlanValue(t, s, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, created.ID),
		"cluster":      tftypes.NewValue(tftypes.String, "dev"),
		"display_name": tftypes.NewValue(tftypes.String, "ci"),
		"access": tftypes.NewValue(accessType, map[string]tftypes.Value{
			"role": tftypes.NewValue(tftypes.String, "admin"),
		}),
		"expires_in_days": tftypes.NewValue(tftypes.Number, 90),
		"expires_at":      tftypes.NewValue(tftypes.String, created.ExpiresAt),
		"created_by":      tftypes.NewValue(tftypes.String, created.CreatedBy),
		"created_at":      tftypes.NewValue(tftypes.String, created.CreatedAt),
		"phase":           tftypes.NewValue(tftypes.String, "Ready"),
		"collected":       tftypes.NewValue(tftypes.Bool, true),
		"token":           tftypes.NewValue(tftypes.String, "tok"),
		"kubeconfig":      tftypes.NewValue(tftypes.String, "kc"),
	})
	state := tfsdk.State{Schema: s.Schema, Raw: raw}

	for _, tt := range []struct {
		name     string
		now      time.Time
		wantWarn bool
	}{
		{name: "before expiry", now: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), wantWarn: false},
		{name: "after expiry", now: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), wantWarn: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prev := clusterAccessTokenNow
			clusterAccessTokenNow = func() time.Time { return tt.now }
			t.Cleanup(func() { clusterAccessTokenNow = prev })
			resp := resource.ReadResponse{State: tfsdk.State{Schema: s.Schema, Raw: state.Raw.Copy()}}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("Read: %v", resp.Diagnostics)
			}
			got := warnings(resp.Diagnostics)
			want := fmt.Sprintf("token %q expired at %s. Once the platform removes it, the next apply creates a new one.",
				created.ID, created.ExpiresAt)
			if tt.wantWarn != (len(got) == 1 && strings.Contains(got[0], want)) || (!tt.wantWarn && len(got) != 0) {
				t.Errorf("warnings = %q, want warning %v (%q)", got, tt.wantWarn, want)
			}
			var after ClusterAccessTokenResourceModel
			if diags := resp.State.Get(ctx, &after); diags.HasError() {
				t.Fatalf("state: %v", diags)
			}
			if after.Token.ValueString() != "tok" || after.Kubeconfig.ValueString() != "kc" {
				t.Error("Read did not keep the token and kubeconfig from state")
			}
		})
	}
}

// TestAccClusterAccessTokenResource_CollectAnswerLost: the platform claims
// the token at this apply's collect and the answer never arrives (here a 500
// after the claim). Create says the token could not be delivered, never that
// it was collected outside the apply; it is tainted and replaced next.
func TestAccClusterAccessTokenResource_CollectAnswerLost(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) { m.catLoseCollectAfterClaim = true })
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{
				Config: config,
				ExpectError: regexp.MustCompile(`(?s)could not be delivered.*served\s+access\s+token\s+"cat-[a-f0-9]{16}"\s+to\s+this\s+apply.*` +
					wrappedText("revokes it and creates a new one.")),
			},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.catLoseCollectAfterClaim = false }) },
				Config:           config,
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionReplace),
				Check:            acc.TestCheckResourceAttr(catAddr, "collected", "true"),
			},
		},
	})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, req := range mock.catRequests {
		if req.method == http.MethodDelete {
			return
		}
	}
	t.Error("the replace never revoked the lost token")
}

// TestAccClusterAccessTokenResource_RevokedDuringWait: a token gone (404 on
// GET) while Create waits for Ready is removed from state at once, not
// polled to the timeout; the next apply creates a new one.
func TestAccClusterAccessTokenResource_RevokedDuringWait(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) {
		m.catPhases = []string{"Pending"}
		m.catRevokeAfterGet = true
	})
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	// firstApply is how many requests the failed apply made.
	firstApply := 0
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)removed before it was collected.*nothing\s+to\s+revoke`),
			},
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						firstApply = len(m.catRequests)
						m.catPhases, m.catRevokeAfterGet = nil, false
					})
				},
				Config:           config,
				ConfigPlanChecks: expectCATAction(plancheck.ResourceActionCreate),
			},
		},
	})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, req := range mock.catRequests[:firstApply] {
		if strings.HasSuffix(req.path, "/token") {
			t.Errorf("a token gone while Pending was collected: %s %s", req.method, req.path)
		}
	}
	if gets := len(mock.catRequests[:firstApply]); gets > 3 {
		t.Errorf("the failed apply made %d requests, want the POST and two GETs (no polling to the timeout)", gets)
	}
}

// TestAccClusterAccessTokenResource_RoutesNotServed: a 404 with no code that
// names nothing (a kupe API without these routes, a misrouted ingress) is an
// error with the hint, never a revoke: refresh keeps the token in state, and
// it is still there once the routes answer again.
func TestAccClusterAccessTokenResource_RoutesNotServed(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	config := testAccClusterAccessTokenConfig(mock.url(), "dev", "ci", "admin", "")
	var id string
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoClusterAccessTokens(mock),
		Steps: []acc.TestStep{
			{Config: config, Check: func(*terraform.State) error { id = catID(mock); return nil }},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.catRoutesMissing = true }) },
				Config:    config,
				ExpectError: regexp.MustCompile(`(?s)failed to read cluster access token.*` +
					wrappedText("does not offer cluster access tokens yet")),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.catRoutesMissing = false }) },
				Config:    config,
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(s *terraform.State) error {
					attrs := s.RootModule().Resources[catAddr].Primary.Attributes
					if attrs["id"] != id || attrs["token"] != mockClusterAccessToken(id) {
						return fmt.Errorf("state lost the token: id %q (was %q), token length %d", attrs["id"], id, len(attrs["token"]))
					}
					return nil
				},
			},
		},
	})
}

// TestClusterAccessTokenDelete_RoutesNotServed: a destroy against an API
// without the routes fails rather than report a revoke that never happened.
func TestClusterAccessTokenDelete_RoutesNotServed(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedCATClusters(mock, "dev")
	mock.setState(func(m *mockKupeAPI) { m.catRoutesMissing = true })
	r := NewClusterAccessTokenResource().(*ClusterAccessTokenResource)
	r.client = client.New(mock.url(), "acme", "kupe_test_key")
	s := testResourceSchema(t, r)
	raw := testCATPlan(t, s)
	state := tfsdk.State{Schema: s.Schema, Raw: raw}
	if diags := state.SetAttribute(context.Background(), path.Root("id"), "cat-0a1b2c3d4e5f6a7b"); diags.HasError() {
		t.Fatal(diags)
	}
	var resp resource.DeleteResponse
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if errs := resp.Diagnostics.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Detail(), "does not offer cluster access tokens yet") {
		t.Fatalf("diagnostics = %v, want the not-served error", resp.Diagnostics)
	}
}
