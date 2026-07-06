package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccSecretResource(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccSecretConfig(mock.url(), "prod", "default", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_secret.test", "name", "db-password"),
					resource.TestCheckResourceAttr("kupe_secret.test", "secret_path", "production/db-password"),
					// Create now waits for phase=Active before returning;
					// the mock advances Pending → Active on first GET.
					resource.TestCheckResourceAttr("kupe_secret.test", "phase", "Active"),
					resource.TestCheckResourceAttr("kupe_secret.test", "sync.#", "1"),
					resource.TestCheckResourceAttr("kupe_secret.test", "sync.0.cluster", "prod"),
					resource.TestCheckResourceAttr("kupe_secret.test", "sync.0.namespace", "default"),
					resource.TestCheckResourceAttrSet("kupe_secret.test", "etag"),
				),
			},
			{
				Config: testAccSecretConfig(mock.url(), "prod", "backend", "database-credentials"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_secret.test", "sync.#", "1"),
					resource.TestCheckResourceAttr("kupe_secret.test", "sync.0.cluster", "prod"),
					resource.TestCheckResourceAttr("kupe_secret.test", "sync.0.namespace", "backend"),
					resource.TestCheckResourceAttr("kupe_secret.test", "sync.0.secret_name", "database-credentials"),
				),
			},
			// Import roundtrip — secret imports by `name`.
			{
				ResourceName:                         "kupe_secret.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateId:                        "db-password",
				ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})
}

// TestAccSecretResourceNoSync is the regression test for a kupe_secret
// declared without a sync block. kupe-api always returns "sync": [] for a
// secret with no targets (never null/absent — the mock mirrors this), so
// the provider must preserve the planned null instead of writing an empty
// list into state; mapping [] verbatim failed every apply with "Provider
// produced inconsistent result after apply" and showed a perpetual
// [] -> null diff on refresh. The step's automatic post-apply refresh/plan
// asserts the no-perpetual-diff half.
func TestAccSecretResourceNoSync(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: testAccSecretNoSyncConfig(mock.url()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_secret.nosync", "name", "api-token"),
					resource.TestCheckResourceAttr("kupe_secret.nosync", "phase", "Active"),
					// sync stays null — not an empty list.
					resource.TestCheckNoResourceAttr("kupe_secret.nosync", "sync.#"),
				),
			},
		},
	})
}

func testAccSecretNoSyncConfig(host string) string {
	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_secret" "nosync" {
  name        = "api-token"
  secret_path = "production/api-token"
}
`, host)
}

func testAccSecretConfig(host, cluster, namespace, secretName string) string {
	secretNameBlock := ""
	if secretName != "" {
		secretNameBlock = fmt.Sprintf("\n      secret_name = %q", secretName)
	}

	return fmt.Sprintf(`
provider "kupe" {
  host    = %q
  tenant  = "acme"
  api_key = "kupe_test_key"
}

resource "kupe_secret" "test" {
  name        = "db-password"
  secret_path = "production/db-password"

  sync = [
    {
      cluster   = %q
      namespace = %q%s
    },
  ]
}
`, host, cluster, namespace, secretNameBlock)
}
