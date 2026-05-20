package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccTenantMemberResource_RoleValidator guards the OneOf validator on
// the `role` attribute. Plan-time rejection, no API call.
func TestAccTenantMemberResource_RoleValidator(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccMemberConfig(mock.url(), "user@acme.com", "superuser"),
				ExpectError: regexp.MustCompile(`value must be one of: \["admin" "readonly"\]`),
			},
		},
	})
}

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

// TestAccTenantMemberResource_MixedCaseEmail guards against a previous
// regression: Create used to lowercase the email server-side and write the
// lowercased value back into state, which the framework rejected as a
// "Provider produced inconsistent result after apply" error whenever the
// user's HCL had any uppercase letter. Read matches case-insensitively, so
// state should keep the user's original casing.
func TestAccTenantMemberResource_MixedCaseEmail(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccMemberConfig(mock.url(), "User@Acme.com", "readonly"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_tenant_member.test", "email", "User@Acme.com"),
					resource.TestCheckResourceAttr("kupe_tenant_member.test", "role", "readonly"),
				),
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
