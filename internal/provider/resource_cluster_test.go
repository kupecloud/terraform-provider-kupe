package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccClusterResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			// Create and read
			{
				Config: testAccClusterConfig(mock.url(), "test-cluster", "Test Cluster", "shared"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_cluster.test", "name", "test-cluster"),
					resource.TestCheckResourceAttr("kupe_cluster.test", "display_name", "Test Cluster"),
					resource.TestCheckResourceAttr("kupe_cluster.test", "type", "shared"),
					resource.TestCheckResourceAttrSet("kupe_cluster.test", "etag"),
				),
			},
			// Update version
			{
				Config: testAccClusterConfigWithVersion(mock.url(), "test-cluster", "Test Cluster", "shared", "1.32"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_cluster.test", "version", "1.32"),
				),
			},
			// Import roundtrip — cluster imports by `name`.
			{
				ResourceName:                         "kupe_cluster.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "test-cluster",
				ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})
}

func testAccClusterConfig(host, name, displayName, clusterType string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_cluster" "test" {
  name         = %q
  display_name = %q
  type         = %q
}
`, host, name, displayName, clusterType)
}

func testAccClusterConfigWithVersion(host, name, displayName, clusterType, version string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_cluster" "test" {
  name         = %q
  display_name = %q
  type         = %q
  version      = %q
}
`, host, name, displayName, clusterType, version)
}
