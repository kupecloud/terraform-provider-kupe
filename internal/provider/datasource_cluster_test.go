package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccClusterDataSource creates a cluster via the resource then reads
// it back through the data source. The composite Config exercises both
// the resource and the data source against the same mock state, so we
// know the data source is reading what the resource wrote.
func TestAccClusterDataSource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccClusterDataSourceConfig(mock.url()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.kupe_cluster.read", "name", "ds-test"),
					resource.TestCheckResourceAttr("data.kupe_cluster.read", "display_name", "DataSource Test"),
					resource.TestCheckResourceAttr("data.kupe_cluster.read", "type", "shared"),
					resource.TestCheckResourceAttr("data.kupe_cluster.read", "version", "1.32"),
					resource.TestCheckResourceAttr("data.kupe_cluster.read", "phase", "Pending"),
				),
			},
		},
	})
}

func testAccClusterDataSourceConfig(host string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_cluster" "src" {
  name         = "ds-test"
  display_name = "DataSource Test"
  type         = "shared"
  version      = "1.32"
}

data "kupe_cluster" "read" {
  name = kupe_cluster.src.name
}
`, host)
}
