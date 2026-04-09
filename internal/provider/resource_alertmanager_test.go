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
