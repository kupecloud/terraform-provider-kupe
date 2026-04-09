package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccAPIKeyResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccAPIKeyConfig(mock.url(), "CI/CD Pipeline", "admin", "2027-01-01T00:00:00Z"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_api_key.test", "display_name", "CI/CD Pipeline"),
					resource.TestCheckResourceAttr("kupe_api_key.test", "role", "admin"),
					resource.TestCheckResourceAttr("kupe_api_key.test", "expires_at", "2027-01-01T00:00:00Z"),
					resource.TestCheckResourceAttrSet("kupe_api_key.test", "id"),
					resource.TestCheckResourceAttrSet("kupe_api_key.test", "key"),
					resource.TestCheckResourceAttr("kupe_api_key.test", "created_by", "test@acme.com"),
					resource.TestCheckResourceAttr("kupe_api_key.test", "created_at", "2024-01-01T00:00:00Z"),
				),
			},
		},
	})
}

func testAccAPIKeyConfig(host, displayName, role, expiresAt string) string {
	expiresAtBlock := ""
	if expiresAt != "" {
		expiresAtBlock = fmt.Sprintf("\n  expires_at   = %q", expiresAt)
	}

	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_api_key" "test" {
  display_name = %q
  role         = %q%s
}
`, host, displayName, role, expiresAtBlock)
}
