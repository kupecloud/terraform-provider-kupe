package provider

import (
	"fmt"
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
				ImportStateId:           "routes",
				ImportStateVerifyIgnore: []string{"routes_json"},
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
			// fixed id "global". body_json excluded from verify for the
			// same JSON-key-ordering reason as receiver above.
			{
				ResourceName:            "kupe_alertmanager_global.main",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateId:           "global",
				ImportStateVerifyIgnore: []string{"body_json"},
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
