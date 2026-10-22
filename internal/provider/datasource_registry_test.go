package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func seedRegistry(mock *mockKupeAPI, limit string) {
	mock.setState(func(m *mockKupeAPI) {
		m.registries["registry"] = map[string]any{
			"name": "registry", "storageLimit": limit, "deletionProtection": true, "generation": 1,
			"status": map[string]any{}, "resourceVersion": "1", "createdAt": "2026-10-21T10:00:00Z",
		}
	})
}

func TestAccRegistryDataSource(t *testing.T) {
	tests := []struct {
		name, block string
	}{
		{name: "implicit name", block: `data "kupe_registry" "test" {}`},
		{name: "explicit name", block: `data "kupe_registry" "test" { name = "registry" }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			seedRegistry(mock, "2Gi")
			const addr = "data.kupe_registry.test"
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []resource.TestStep{{
					Config: testAccProviderConfig(mock.url()) + tt.block,
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(addr, "name", "registry"),
						resource.TestCheckResourceAttr(addr, "storage_limit", "2Gi"),
						resource.TestCheckResourceAttr(addr, "storage_limit_bytes", "2147483648"),
						resource.TestCheckResourceAttr(addr, "deletion_protection", "true"),
						resource.TestCheckResourceAttr(addr, "endpoint", "registry.kupe.cloud/acme"),
						resource.TestCheckResourceAttr(addr, "phase", "Running"),
						resource.TestCheckResourceAttr(addr, "repository_count", "2"),
						resource.TestCheckResourceAttr(addr, "storage_used_bytes", "1048576"),
						resource.TestCheckResourceAttr(addr, "created_at", "2026-10-21T10:00:00Z"),
					),
				}},
			})
		})
	}
}

func TestAccRegistryDataSource_OtherNameRejected(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{{
			Config:      testAccProviderConfig(mock.url()) + `data "kupe_registry" "test" { name = "images" }`,
			ExpectError: regexp.MustCompile(`a tenant has one registry, named\s+registry`),
		}},
	})
}

func TestAccRegistryDataSource_NoRegistry(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{{
			Config:      testAccProviderConfig(mock.url()) + `data "kupe_registry" "test" {}`,
			ExpectError: regexp.MustCompile(`(?s)no registry.*This tenant has no registry`),
		}},
	})
}
