package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccAlertmanagerReceiverResource exercises the receiver lifecycle
// (create, update body, delete) against the in-memory mock kupe API.
func TestAccAlertmanagerReceiverResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccReceiverConfig(mock.url(), `{"slack_configs":[{"channel":"#alerts"}]}`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_receiver.slack", "name", "slack"),
					resource.TestCheckResourceAttrSet("kupe_alertmanager_receiver.slack", "etag"),
				),
			},
			{
				Config: testAccReceiverConfig(mock.url(), `{"slack_configs":[{"channel":"#noisy"}]}`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_receiver.slack", "name", "slack"),
				),
			},
			// Import roundtrip — receiver imports by `name`.
			//
			// `body_json` is excluded from verify: the framework's
			// ImportStateVerify byte-compares state attrs, but the JSON
			// comes back from the API with alphabetised keys vs the
			// user's original key ordering. The provider's JSONStringType
			// custom semantic-equality fixes this at plan time, just not
			// at import-verify time.
			{
				ResourceName:                         "kupe_alertmanager_receiver.slack",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "slack",
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"body_json"},
			},
		},
	})
}

// TestAccAlertmanagerReceiverMaskedSecrets is the regression test for the
// kupe-api KA-1 secret masking: the server replaces credential-bearing
// values ("api_url", passwords, tokens, …) with the sentinel "<secret>" in
// both GET and PUT responses. The provider must (a) keep the planned
// body_json on Create/Update instead of storing the masked echo (apply
// consistency), (b) unmask Reads against prior state so refresh produces
// no perpetual diff, and (c) still pick up genuine remote changes to
// non-secret fields. The mock masks its responses exactly like kupe-api.
func TestAccAlertmanagerReceiverMaskedSecrets(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	const apiURL = "https://hooks.slack.com/services/T00/B00/secret123"
	bodyAlerts := fmt.Sprintf(`{"slack_configs":[{"api_url":%q,"channel":"#alerts"}]}`, apiURL)
	bodyNoisy := fmt.Sprintf(`{"slack_configs":[{"api_url":%q,"channel":"#noisy"}]}`, apiURL)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			// Create: previously failed with "Provider produced
			// inconsistent result after apply" because the masked PUT echo
			// was mapped into state. The step's automatic post-apply
			// refresh/plan also proves the masked GET does not produce a
			// perpetual diff.
			{
				Config: testAccReceiverConfig(mock.url(), bodyAlerts),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_receiver.slack", "body_json", bodyAlerts),
					resource.TestCheckResourceAttrSet("kupe_alertmanager_receiver.slack", "etag"),
				),
			},
			// Update of a non-secret field alongside the (still masked)
			// secret: the real URL must survive in state.
			{
				Config: testAccReceiverConfig(mock.url(), bodyNoisy),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_receiver.slack", "body_json", bodyNoisy),
				),
			},
			// Genuine remote change to a non-secret field must still be
			// detected: refresh picks up the new channel while restoring
			// the real api_url from prior state (keys alphabetised by the
			// Read round-trip through encoding/json).
			{
				PreConfig: func() {
					mock.mutateReceiver("slack", func(r map[string]any) {
						r["slack_configs"].([]any)[0].(map[string]any)["channel"] = "#ops"
					})
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_receiver.slack", "body_json",
						fmt.Sprintf(`{"slack_configs":[{"api_url":%q,"channel":"#ops"}]}`, apiURL)),
				),
			},
			// Applying the config again converges the remote drift.
			{
				Config: testAccReceiverConfig(mock.url(), bodyNoisy),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_receiver.slack", "body_json", bodyNoisy),
				),
			},
		},
	})
}

// TestAccAlertmanagerReceiverCreateRejectsExisting covers MEDIUM-3: a
// Create against a receiver name that already exists on the server (authored
// via the Console or another workspace) must error and direct the user to
// import, not silently overwrite the pre-existing config.
func TestAccAlertmanagerReceiverCreateRejectsExisting(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.seedReceiver("slack", map[string]any{
		"slack_configs": []any{map[string]any{"channel": "#preexisting"}},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccReceiverConfig(mock.url(), `{"slack_configs":[{"channel":"#alerts"}]}`),
				ExpectError: regexp.MustCompile(`already exists`),
			},
		},
	})
}

func testAccReceiverConfig(host, body string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_alertmanager_receiver" "slack" {
  name      = "slack"
  body_json = %q
}
`, host, body)
}

// TestAccAlertmanagerRoutesResource exercises the singleton routes
// resource — initial creation, mutation, and deletion all replace the
// full child route list atomically via the kupe-api PUT endpoint.
func TestAccAlertmanagerRoutesResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	first := `[{"receiver":"slack","matchers":["severity=\"critical\""]}]`
	second := `[{"receiver":"slack","matchers":["severity=\"warning\""]},{"receiver":"slack","matchers":["team=\"infra\""]}]`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccRoutesConfig(mock.url(), first),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kupe_alertmanager_routes.main", "etag"),
				),
			},
			{
				Config: testAccRoutesConfig(mock.url(), second),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kupe_alertmanager_routes.main", "etag"),
				),
			},
			// Import roundtrip — routes is a singleton, imported by the
			// fixed id "routes". routes_json excluded from verify for the
			// same JSON-key-ordering reason as receiver above.
			{
				ResourceName:            "kupe_alertmanager_routes.main",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateId:           "alertmanager-routes",
				ImportStateVerifyIgnore: []string{"routes_json"},
			},
		},
	})
}

// TestAccAlertmanagerRoutesCreateRejectsExisting covers MEDIUM-3 for the
// routes singleton: a Create when the tenant already has a child route list
// must error and direct the user to import rather than replacing the list.
func TestAccAlertmanagerRoutesCreateRejectsExisting(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.seedRoutes([]map[string]any{{"receiver": "existing", "matchers": []any{`team="infra"`}}})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccRoutesConfig(mock.url(), `[{"receiver":"slack","matchers":["severity=\"critical\""]}]`),
				ExpectError: regexp.MustCompile(`already exist`),
			},
		},
	})
}

func testAccRoutesConfig(host, routes string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_alertmanager_routes" "main" {
  routes_json = %q
}
`, host, routes)
}

// TestAccAlertmanagerGlobalResource exercises the singleton global section.
func TestAccAlertmanagerGlobalResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	first := `{"smtp_from":"alerts@example.com","resolve_timeout":"5m"}`
	second := `{"smtp_from":"ops@example.com","resolve_timeout":"10m","smtp_smarthost":"smtp.example.com:587"}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccGlobalConfig(mock.url(), first),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kupe_alertmanager_global.main", "etag"),
				),
			},
			{
				Config: testAccGlobalConfig(mock.url(), second),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kupe_alertmanager_global.main", "etag"),
				),
			},
			// Import roundtrip — global is a singleton, imported by the
			// fixed id "alertmanager-global". body_json excluded from
			// verify for the same JSON-key-ordering reason as receiver
			// above.
			{
				ResourceName:            "kupe_alertmanager_global.main",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateId:           "alertmanager-global",
				ImportStateVerifyIgnore: []string{"body_json"},
			},
		},
	})
}

// TestAccAlertmanagerGlobalMaskedSecrets is the global-section companion
// to TestAccAlertmanagerReceiverMaskedSecrets: smtp_auth_password and
// slack_api_url come back masked from the mock on every response, so
// apply consistency and refresh stability both depend on the provider
// keeping planned values on write and unmasking reads against state.
func TestAccAlertmanagerGlobalMaskedSecrets(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	first := `{"smtp_from":"alerts@example.com","smtp_auth_password":"hunter2","slack_api_url":"https://hooks.slack.com/services/T00/B00/secret123"}`
	second := `{"smtp_from":"ops@example.com","smtp_auth_password":"hunter2","slack_api_url":"https://hooks.slack.com/services/T00/B00/secret123"}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccGlobalConfig(mock.url(), first),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_global.main", "body_json", first),
					resource.TestCheckResourceAttrSet("kupe_alertmanager_global.main", "etag"),
				),
			},
			// Update a non-secret field; the credentials must survive both
			// the masked PUT echo and the post-apply refresh.
			{
				Config: testAccGlobalConfig(mock.url(), second),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_alertmanager_global.main", "body_json", second),
				),
			},
		},
	})
}

// TestAccAlertmanagerGlobalCreateRejectsExisting covers MEDIUM-3 for the
// global singleton: a Create when the tenant already has a non-empty global
// section must error and direct the user to import.
func TestAccAlertmanagerGlobalCreateRejectsExisting(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.seedGlobal(map[string]any{"smtp_from": "preexisting@example.com"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccGlobalConfig(mock.url(), `{"smtp_from":"alerts@example.com"}`),
				ExpectError: regexp.MustCompile(`already exists`),
			},
		},
	})
}

func testAccGlobalConfig(host, body string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_alertmanager_global" "main" {
  body_json = %q
}
`, host, body)
}
