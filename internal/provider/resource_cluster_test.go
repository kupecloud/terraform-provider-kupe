package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
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
			// Import roundtrip — cluster imports by `name`. display_name is a
			// deprecated config-only attribute (never read from the API), so
			// it is absent after import by design.
			{
				ResourceName:                         "kupe_cluster.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "test-cluster",
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"display_name"},
			},
		},
	})
}

// TestAccClusterResource_DisplayNameOptional guards the 2026-08-18 contract
// fix: `display_name` is deprecated/optional and the API echoes the cluster
// name in `displayName`. A cluster created WITHOUT display_name must apply
// cleanly (state null, no "inconsistent result after apply"), and adding or
// removing display_name later must never force replacement — the mock's
// nextRV/createdAt would change on a recreate, so createdAt is pinned.
func TestAccClusterResource_DisplayNameOptional(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccClusterConfigNoDisplayName(mock.url(), "nodisplay"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_cluster.test", "name", "nodisplay"),
					resource.TestCheckNoResourceAttr("kupe_cluster.test", "display_name"),
				),
			},
			{
				// Adding a display_name is an in-place, API-less change.
				Config: testAccClusterConfig(mock.url(), "nodisplay", "Legacy Display", "shared"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("kupe_cluster.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("kupe_cluster.test", "display_name", "Legacy Display"),
			},
			{
				Config: testAccClusterConfigNoDisplayName(mock.url(), "nodisplay"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("kupe_cluster.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckNoResourceAttr("kupe_cluster.test", "display_name"),
			},
		},
	})
}

func testAccClusterConfigNoDisplayName(host, name string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "test-key"
}

resource "kupe_cluster" "test" {
  name = %q
}
`, host, name)
}

// TestAccClusterResource_VersionNotBlankedOnUnrelatedEdit is the end-to-end
// regression guard for TPK-1 / INT-1: a user who set `version` once and then
// drops it from config while editing an unrelated attribute (a `resources`
// block) must NOT have the version blanked on the wire. The original bug:
// `version` was Optional+Computed with no plan modifier, so removing it from
// config planned it as unknown, the Update guard fired, and the provider
// PATCHed `{"version":""}` — which kupe-api resolved to a default version,
// silently changing the tenant cluster's Kubernetes minor version on an edit
// the user never made to version. UseStateForUnknown + the IsUnknown guard
// close it. This test exercises the FULL framework plan→apply path (unlike
// the client-level TestUpdateCluster_OmitsVersionWhenNil which only pins the
// serialisation): if version were blanked, the mock's PATCH would overwrite
// its stored version to "" and the post-update version check would fail.
func TestAccClusterResource_VersionNotBlankedOnUnrelatedEdit(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			// Create with version explicitly set (a "non-default supported
			// version" tenant, e.g. sitting on 1.30 while newer is default).
			{
				Config: testAccClusterConfigWithVersion(mock.url(), "ver-cluster", "Ver Cluster", "shared", "1.30"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_cluster.test", "version", "1.30"),
				),
			},
			// Drop version from config AND change an unrelated attribute
			// (add a resources block). version must stay 1.30 — proving the
			// provider did not send {"version":""} on this edit.
			{
				Config: testAccClusterConfigResourcesNoVersion(mock.url(), "ver-cluster", "Ver Cluster", "shared", "4"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_cluster.test", "version", "1.30"),
					resource.TestCheckResourceAttr("kupe_cluster.test", "resources.cpu", "4"),
				),
			},
		},
	})
}

// TestAccClusterResource_EmptyResourcesBlock is the LOW-4 regression guard:
// a `resources = {}` block (used to request clearing the limits) must apply
// cleanly. The server echoes an empty `{}`; before the fix mapClusterToState
// collapsed that to nil, turning the planned known object into null and
// failing "Provider produced inconsistent result after apply". The step's
// automatic post-apply refresh/plan also asserts there's no perpetual drift.
func TestAccClusterResource_EmptyResourcesBlock(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccClusterConfigEmptyResources(mock.url(), "empty-res", "Empty Res"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_cluster.test", "name", "empty-res"),
					// The block is present (not null) but every limit is unset.
					resource.TestCheckNoResourceAttr("kupe_cluster.test", "resources.cpu"),
					resource.TestCheckNoResourceAttr("kupe_cluster.test", "resources.memory"),
					resource.TestCheckNoResourceAttr("kupe_cluster.test", "resources.storage"),
				),
			},
		},
	})
}

// testAccClusterConfigEmptyResources sets an explicitly empty `resources`
// block to exercise the LOW-4 clear-the-limits path.
func testAccClusterConfigEmptyResources(host, name, displayName string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_cluster" "test" {
  name         = %q
  display_name = %q
  resources    = {}
}
`, host, name, displayName)
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

// testAccClusterConfigResourcesNoVersion omits `version` entirely (relying on
// UseStateForUnknown to retain the prior value) while setting a `resources`
// block, to exercise the TPK-1 "unrelated edit with version unset" path.
func testAccClusterConfigResourcesNoVersion(host, name, displayName, clusterType, cpu string) string {
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
  resources = {
    cpu = %q
  }
}
`, host, name, displayName, clusterType, cpu)
}
