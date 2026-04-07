package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccTenantMemberResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"kupe": providerserver.NewProtocol6WithError(New("test")()),
		},
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
