package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const tokenAddr = "kupe_registry_token.test"

// tokenSecretPattern is the platform's secret format (registry contract §5).
var tokenSecretPattern = regexp.MustCompile(`^kupecr_[0-9A-Za-z]{42}$`)

func testAccRegistryTokenConfig(host, displayName, access, extra string) string {
	return testAccProviderConfig(host) + fmt.Sprintf(`
resource "kupe_registry_token" "test" {
  display_name = %q
  access       = %q
%s}
`, displayName, access, extra)
}

func testAccCheckNoRegistryTokens(mock *mockKupeAPI) acc.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.registryTokens) != 0 {
			return fmt.Errorf("expected no access tokens, got %d", len(mock.registryTokens))
		}
		return nil
	}
}

// tokenID is the id of the one token in the mock.
func tokenID(mock *mockKupeAPI) string {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for id := range mock.registryTokens {
		return id
	}
	return ""
}

func expectTokenAction(action plancheck.ResourceActionType) acc.ConfigPlanChecks {
	return acc.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(tokenAddr, action)},
	}
}

func TestAccRegistryTokenResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	config := testAccRegistryTokenConfig(mock.url(), "GitHub Actions", "push", "  expires_in_days = 30\n")
	var secret string

	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryTokens(mock),
		Steps: []acc.TestStep{
			{
				Config: config,
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestMatchResourceAttr(tokenAddr, "id", validRegistryTokenID),
					acc.TestCheckResourceAttr(tokenAddr, "registry", "registry"),
					acc.TestCheckResourceAttr(tokenAddr, "expires_in_days", "30"),
					acc.TestCheckResourceAttr(tokenAddr, "registry_host", "registry.kupe.cloud"),
					acc.TestMatchResourceAttr(tokenAddr, "username", regexp.MustCompile(`^kupe\+acme\+tok-[a-f0-9]{16}$`)),
					acc.TestCheckResourceAttr(tokenAddr, "created_at", "2026-10-21T10:00:00Z"),
					acc.TestCheckResourceAttr(tokenAddr, "expires_at", "2026-11-20T10:00:00Z"),
					acc.TestCheckResourceAttr(tokenAddr, "created_by", "admin@acme.com"),
					acc.TestCheckResourceAttr(tokenAddr, "phase", "Ready"),
					acc.TestCheckNoResourceAttr(tokenAddr, "last_used_at"),
					acc.TestMatchResourceAttr(tokenAddr, "secret", tokenSecretPattern),
					func(s *terraform.State) error {
						secret = s.RootModule().Resources[tokenAddr].Primary.Attributes["secret"]
						body := lastRegistryBody(t, mock, "POST", mockRegistriesPath+"/registry/tokens")
						if body["expiresInDays"] != float64(30) || body["access"] != "push" || body["displayName"] != "GitHub Actions" {
							return fmt.Errorf("POST body = %v, want expiresInDays 30", body)
						}
						return nil
					},
				),
			},
			{
				// The platform reports its own username and a first use: a
				// refresh records both, keeps the secret and plans nothing.
				PreConfig: func() {
					id := tokenID(mock)
					mock.setState(func(m *mockKupeAPI) {
						m.registryTokens[id]["username"] = "kupe+acme+tok-issued"
						m.registryTokens[id]["lastUsedAt"] = "2026-10-22T08:00:00Z"
					})
				},
				Config: config,
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(tokenAddr, "username", "kupe+acme+tok-issued"),
					acc.TestCheckResourceAttr(tokenAddr, "last_used_at", "2026-10-22T08:00:00Z"),
					func(s *terraform.State) error {
						if got := s.RootModule().Resources[tokenAddr].Primary.Attributes["secret"]; got != secret || got == "" {
							return fmt.Errorf("secret changed on refresh (length %d, was %d)", len(got), len(secret))
						}
						return nil
					},
				),
			},
			{
				// The secret is not recoverable: null after import.
				ResourceName:            tokenAddr,
				ImportState:             true,
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return "registry/" + tokenID(mock), nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret", "registry_host"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances", len(states))
					}
					attrs := states[0].Attributes
					if v, ok := attrs["secret"]; ok && v != "" {
						return fmt.Errorf("secret after import has length %d, want null", len(v))
					}
					if attrs["expires_in_days"] != "30" {
						return fmt.Errorf("expires_in_days after import = %q, want 30", attrs["expires_in_days"])
					}
					return nil
				},
			},
			{
				// The token was revoked outside Terraform: a refresh removes
				// it and the plan creates a new one.
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.registryTokens = map[string]map[string]any{} })
				},
				Config:           config,
				ConfigPlanChecks: expectTokenAction(plancheck.ResourceActionCreate),
				Check: func(s *terraform.State) error {
					if got := s.RootModule().Resources[tokenAddr].Primary.Attributes["secret"]; got == secret {
						return fmt.Errorf("the new token kept the old secret")
					}
					return nil
				},
			},
		},
	})
}

// TestAccRegistryTokenResource_DefaultLifetime: without expires_in_days the
// request omits it and state records the platform's 90 days.
func TestAccRegistryTokenResource_DefaultLifetime(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	config := testAccRegistryTokenConfig(mock.url(), "ci", "pull", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryTokens(mock),
		Steps: []acc.TestStep{
			{
				Config: config,
				Check: acc.ComposeAggregateTestCheckFunc(
					acc.TestCheckResourceAttr(tokenAddr, "expires_in_days", "90"),
					acc.TestCheckResourceAttr(tokenAddr, "expires_at", "2027-01-19T10:00:00Z"),
					func(*terraform.State) error {
						body := lastRegistryBody(t, mock, "POST", mockRegistriesPath+"/registry/tokens")
						if _, sent := body["expiresInDays"]; sent {
							return fmt.Errorf("POST body = %v, want no expiresInDays", body)
						}
						return nil
					},
				),
			},
			{
				// Naming the default explicitly changes nothing.
				Config: testAccRegistryTokenConfig(mock.url(), "ci", "pull", "  expires_in_days = 90\n"),
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A timeout change is recorded in place, not a new token.
				Config:           testAccRegistryTokenConfig(mock.url(), "ci", "pull", "  timeouts = { create = \"10m\" }\n"),
				ConfigPlanChecks: expectTokenAction(plancheck.ResourceActionUpdate),
			},
		},
	})
}

// TestAccRegistryTokenResource_InputChangesReplace: every input replaces the
// token.
func TestAccRegistryTokenResource_InputChangesReplace(t *testing.T) {
	tests := []struct {
		name, displayName, access, extra string
	}{
		{name: "display_name", displayName: "renamed", access: "pull"},
		{name: "access", displayName: "ci", access: "push"},
		{name: "expires_in_days", displayName: "ci", access: "pull", extra: "  expires_in_days = 7\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedRegistry(mock, "")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoRegistryTokens(mock),
				Steps: []acc.TestStep{
					{Config: testAccRegistryTokenConfig(mock.url(), "ci", "pull", "")},
					{
						Config:           testAccRegistryTokenConfig(mock.url(), tt.displayName, tt.access, tt.extra),
						ConfigPlanChecks: expectTokenAction(plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			})
		})
	}
}

func TestAccRegistryTokenResource_PlanTimeErrors(t *testing.T) {
	tests := []struct {
		name, displayName, access, extra, wantErr string
	}{
		{name: "lifetime 0", displayName: "ci", access: "pull", extra: "  expires_in_days = 0\n", wantErr: `between 1 and 365`},
		{name: "lifetime 366", displayName: "ci", access: "pull", extra: "  expires_in_days = 366\n", wantErr: `between 1 and 365`},
		{name: "empty name", displayName: "", access: "pull", wantErr: `character count must be between 1 and 128`},
		{name: "129 characters", displayName: strings.Repeat("é", 129), access: "pull", wantErr: `character count must be between 1 and 128`},
		{name: "access", displayName: "ci", access: "admin", wantErr: `value must be one of`},
		{name: "another registry", displayName: "ci", access: "pull", extra: "  registry = \"images\"\n", wantErr: `a tenant has one registry, named\s+registry`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedRegistry(mock, "")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []acc.TestStep{{
					Config:      testAccRegistryTokenConfig(mock.url(), tt.displayName, tt.access, tt.extra),
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
			if len(mock.registryRequests) != 0 {
				t.Errorf("the plan reached the API: %d registry requests", len(mock.registryRequests))
			}
		})
	}
}

// TestAccRegistryTokenResource_128Characters: the limit counts characters,
// not bytes, as the platform does.
func TestAccRegistryTokenResource_128Characters(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryTokens(mock),
		Steps: []acc.TestStep{{
			Config: testAccRegistryTokenConfig(mock.url(), strings.Repeat("é", 128), "pull", ""),
		}},
	})
}

// TestAccRegistryTokenResource_ReadsUnavailableKeepState: registries_unavailable
// on refresh is terminal, not gone: the token stays in state.
func TestAccRegistryTokenResource_ReadsUnavailableKeepState(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	config := testAccRegistryTokenConfig(mock.url(), "ci", "pull", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryTokens(mock),
		Steps: []acc.TestStep{
			{Config: config},
			{
				PreConfig:   func() { mock.setState(func(m *mockKupeAPI) { m.registryReadsUnavailable = true }) },
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)failed to read access token.*` + wrappedText("Registries are not enabled")),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.registryReadsUnavailable = false }) },
				Config:    config,
				ConfigPlanChecks: acc.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccRegistryTokenResource_LimitReached(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	mock.setState(func(m *mockKupeAPI) { m.registryTokenLimit = 0 })
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []acc.TestStep{{
			Config: testAccRegistryTokenConfig(mock.url(), "ci", "pull", ""),
			ExpectError: regexp.MustCompile(`(?s)failed to create access token.*Error code: REGISTRY_TOKEN_LIMIT_REACHED.*` +
				wrappedText("Revoke one you no longer use")),
		}},
	})
}

// TestAccRegistryTokenResource_NoRegistryYet (F-2): a token created before
// the registry exists gets the hint to reference kupe_registry.this.
func TestAccRegistryTokenResource_NoRegistryYet(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []acc.TestStep{{
			Config:      testAccRegistryTokenConfig(mock.url(), "ci", "pull", ""),
			ExpectError: regexp.MustCompile(`(?s)failed to create .*no\s+registry\s+yet.*kupe_registry\.this\.name`),
		}},
	})
}

// TestAccRegistryTokenResource_FailedOnCreate: a token the platform cannot
// issue fails the apply; it stays in state, tainted, and is replaced next.
func TestAccRegistryTokenResource_FailedOnCreate(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	mock.setState(func(m *mockKupeAPI) { m.registryTokenPhases = []string{"Pending", "Failed"} })
	config := testAccRegistryTokenConfig(mock.url(), "ci", "pull", "")
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryTokens(mock),
		Steps: []acc.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)access token failed.*can never\s+work.*Status message: the access token could\s+not be issued.*marked\s+tainted`),
			},
			{
				PreConfig:        func() { mock.setState(func(m *mockKupeAPI) { m.registryTokenPhases = nil }) },
				Config:           config,
				ConfigPlanChecks: expectTokenAction(plancheck.ResourceActionReplace),
			},
		},
	})
}

// TestAccRegistryTokenResource_WaitsUntilIssued: Create keeps polling while
// the token is Pending and ends once it is Ready.
func TestAccRegistryTokenResource_WaitsUntilIssued(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	mock.setState(func(m *mockKupeAPI) { m.registryTokenPhases = []string{"", "Pending", "Pending", "Ready"} })
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryTokens(mock),
		Steps: []acc.TestStep{{
			Config: testAccRegistryTokenConfig(mock.url(), "ci", "pull", ""),
			Check:  acc.TestCheckResourceAttr(tokenAddr, "phase", "Ready"),
		}},
	})
}

// TestAccRegistryTokenResource_ImportRejectsOtherIDs checks the import ID.
func TestAccRegistryTokenResource_ImportRejectsOtherIDs(t *testing.T) {
	for _, id := range []string{"rt-0a1b2c3d4e5f6a7b", "images/rt-0a1b2c3d4e5f6a7b", "registry/ak-0a1b2c3d4e5f6a7b", "registry/rt-0A1B"} {
		t.Run(id, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedRegistry(mock, "")
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				CheckDestroy:             testAccCheckNoRegistryTokens(mock),
				Steps: []acc.TestStep{
					{Config: testAccRegistryTokenConfig(mock.url(), "ci", "pull", "")},
					{
						ResourceName:  tokenAddr,
						ImportState:   true,
						ImportStateId: id,
						ExpectError:   regexp.MustCompile(`expected "registry/<id>"`),
					},
				},
			})
		})
	}
}

// TestAccRegistryTokenResource_SecretNotLogged: provider logs at TRACE carry
// the token's id but never its secret.
func TestAccRegistryTokenResource_SecretNotLogged(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "acc.log")
	t.Setenv("TF_LOG", "TRACE")
	t.Setenv("TF_ACC_LOG_PATH", logPath)
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	var id, secret string
	acc.UnitTest(t, acc.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoRegistryTokens(mock),
		Steps: []acc.TestStep{{
			Config: testAccRegistryTokenConfig(mock.url(), "ci", "push", ""),
			Check: func(s *terraform.State) error {
				attrs := s.RootModule().Resources[tokenAddr].Primary.Attributes
				id, secret = attrs["id"], attrs["secret"]
				return nil
			},
		}},
	})
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !tokenSecretPattern.MatchString(secret) {
		t.Fatalf("secret in state does not have the platform's format (length %d)", len(secret))
	}
	// Control: the provider's own create log line was captured.
	if !strings.Contains(string(logs), "created registry access token") || !strings.Contains(string(logs), id) {
		t.Fatalf("the provider's create log line with id %s is not in the captured logs (%d bytes)", id, len(logs))
	}
	if strings.Contains(string(logs), secret) {
		t.Errorf("the token's secret (length %d) appears in the captured logs", len(secret))
	}
}

// TestRegistryTokenSchema_SecretSensitive pins the secret's schema: computed,
// Sensitive, described verbatim.
func TestRegistryTokenSchema_SecretSensitive(t *testing.T) {
	s := testResourceSchema(t, NewRegistryTokenResource())
	secret, ok := s.Schema.Attributes["secret"].(schema.StringAttribute)
	if !ok || !secret.Sensitive || !secret.Computed || secret.Optional || secret.Required {
		t.Fatalf("secret attribute = %+v, want computed and Sensitive", s.Schema.Attributes["secret"])
	}
	if !strings.Contains(secret.Description, "stored in Terraform state in plain text") {
		t.Errorf("secret description %q does not say it is stored in state in plain text", secret.Description)
	}
	doc, err := os.ReadFile("../../docs/resources/registry_token.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), registryTokenSecretDescription) {
		t.Error("docs/resources/registry_token.md does not state the secret description verbatim; run make docs")
	}
}

// TestRegistryTokenRead_ExpiredWarns: a token past its expiry that the
// platform has not removed yet warns on refresh and stays in state.
func TestRegistryTokenRead_ExpiredWarns(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seedRegistry(mock, "")
	ctx := context.Background()
	c := client.New(mock.url(), "acme", "kupe_test_key")
	created, err := c.CreateRegistryToken(ctx, "registry", client.CreateRegistryTokenRequest{DisplayName: "ci", Access: "pull"})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistryTokenResource().(*RegistryTokenResource)
	r.client = c
	s := testResourceSchema(t, r)
	raw := testPlanValue(t, s, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, created.ID),
		"registry":        tftypes.NewValue(tftypes.String, "registry"),
		"display_name":    tftypes.NewValue(tftypes.String, "ci"),
		"access":          tftypes.NewValue(tftypes.String, "pull"),
		"expires_in_days": tftypes.NewValue(tftypes.Number, 90),
		"username":        tftypes.NewValue(tftypes.String, created.Username),
		"registry_host":   tftypes.NewValue(tftypes.String, created.Registry),
		"expires_at":      tftypes.NewValue(tftypes.String, created.ExpiresAt),
		"created_by":      tftypes.NewValue(tftypes.String, created.CreatedBy),
		"created_at":      tftypes.NewValue(tftypes.String, created.CreatedAt),
		"last_used_at":    tftypes.NewValue(tftypes.String, nil),
		"phase":           tftypes.NewValue(tftypes.String, "Ready"),
		"secret":          tftypes.NewValue(tftypes.String, created.Secret),
	})
	state := tfsdk.State{Schema: s.Schema, Raw: raw}

	for _, tt := range []struct {
		name     string
		now      time.Time
		wantWarn bool
	}{
		{name: "before expiry", now: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), wantWarn: false},
		{name: "after expiry", now: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), wantWarn: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prev := registryTokenNow
			registryTokenNow = func() time.Time { return tt.now }
			t.Cleanup(func() { registryTokenNow = prev })
			resp := resource.ReadResponse{State: tfsdk.State{Schema: s.Schema, Raw: state.Raw.Copy()}}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("Read: %v", resp.Diagnostics)
			}
			got := warnings(resp.Diagnostics)
			want := "token " + fmt.Sprintf("%q", created.ID) + " expired at " + created.ExpiresAt +
				"; it will be replaced on the next apply after the platform removes it."
			if tt.wantWarn != (len(got) == 1 && strings.Contains(got[0], want)) || (!tt.wantWarn && len(got) != 0) {
				t.Errorf("warnings = %q, want warning %v (%q)", got, tt.wantWarn, want)
			}
			if resp.State.Raw.IsNull() {
				t.Error("an expired token was removed from state")
			}
		})
	}
}
