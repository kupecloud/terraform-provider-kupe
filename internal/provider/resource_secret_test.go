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
					resource.TestCheckResourceAttr("kupe_secret.test", "phase", "Pending"),
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
