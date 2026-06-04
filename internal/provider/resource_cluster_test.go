package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccClusterResource_TypeValidator guards the stringvalidator.OneOf on
// the `type` attribute. The validator runs at plan time, so the bad config
// never reaches the API — ExpectError matches the regex against the plan
// diagnostic. Only "shared" is accepted; `dedicated` is rejected at the
// provider plan stage (and again server-side as
// CLUSTER_DEDICATED_UNSUPPORTED if it somehow slipped through).
func TestAccClusterResource_TypeValidator(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccClusterConfig(mock.url(), "bad-type", "Bad Type", "dedicated"),
				ExpectError: regexp.MustCompile(`value must be one of: \["shared"\]`),
			},
		},
	})
}

// TestAccClusterResource_TypeDefaultsToShared verifies the StaticString
// default kicks in when HCL omits `type`. Without the default, dropping
// the now-deprecated attribute from existing configs would surface a
// "value will be known after apply" plan churn on every apply.
func TestAccClusterResource_TypeDefaultsToShared(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccClusterConfigNoType(mock.url(), "default-type", "Default Type"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_cluster.test", "type", "shared"),
				),
			},
		},
	})
}

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

// testAccClusterConfigNoType omits the deprecated `type` attribute to
// exercise the StaticString("shared") default. Once `type` is removed
// from the schema this becomes the only shape of the config.
func testAccClusterConfigNoType(host, name, displayName string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_cluster" "test" {
  name         = %q
  display_name = %q
}
`, host, name, displayName)
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
