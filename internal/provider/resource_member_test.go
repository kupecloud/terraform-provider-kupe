package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccTenantMemberResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			// Create
			{
				Config: testAccMemberConfig(mock.url(), "dev@acme.com", "readonly"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_tenant_member.test", "email", "dev@acme.com"),
					resource.TestCheckResourceAttr("kupe_tenant_member.test", "role", "readonly"),
				),
			},
			// Update role
			{
				Config: testAccMemberConfig(mock.url(), "dev@acme.com", "admin"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_tenant_member.test", "role", "admin"),
				),
			},
			// Import roundtrip — member imports by `email`.
			{
				ResourceName:                         "kupe_tenant_member.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "dev@acme.com",
				ImportStateVerifyIdentifierAttribute: "email",
			},
		},
	})
}

func testAccMemberConfig(host, email, role string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_tenant_member" "test" {
  email = %q
  role  = %q
}
`, host, email, role)
}
