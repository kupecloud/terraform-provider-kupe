package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccTenantDataSource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

data "kupe_tenant" "current" {}
`, mock.url()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.kupe_tenant.current", "name", "acme"),
					resource.TestCheckResourceAttr("data.kupe_tenant.current", "plan", "starter"),
					resource.TestCheckResourceAttr("data.kupe_tenant.current", "display_name", "Acme Corp"),
				),
			},
		},
	})
}

func TestAccPlanDataSource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

data "kupe_plan" "starter" {
  name = "starter"
}
`, mock.url()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.kupe_plan.starter", "display_name", "Starter"),
					resource.TestCheckResourceAttr("data.kupe_plan.starter", "platform_fee", "29.00"),
				),
			},
		},
	})
}
