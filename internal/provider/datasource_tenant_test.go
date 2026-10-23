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
	tests := []struct {
		name string
		// maxDatabases is the plan's maxDatabases in the API response;
		// nil leaves it out, as an older kupe-api does.
		maxDatabases any
		// registry is the plan's registry allowance; nil leaves it out, as a
		// plan without a registry (or an older kupe-api) does.
		registry map[string]any
		check    resource.TestCheckFunc
	}{
		{
			name:         "limited plan",
			maxDatabases: float64(1),
			check:        resource.TestCheckResourceAttr("data.kupe_plan.starter", "max_databases", "1"),
		},
		{
			name:         "unlimited plan reports 0 like max_clusters",
			maxDatabases: float64(0),
			check:        resource.TestCheckResourceAttr("data.kupe_plan.starter", "max_databases", "0"),
		},
		{
			name:  "limit not reported stays null",
			check: resource.TestCheckNoResourceAttr("data.kupe_plan.starter", "max_databases"),
		},
		{
			name:     "registry allowance",
			registry: map[string]any{"maxStorage": "5Gi", "includedStorage": "1Gi"},
			check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kupe_plan.starter", "registry_max_storage", "5Gi"),
				resource.TestCheckResourceAttr("data.kupe_plan.starter", "registry_included_storage", "1Gi"),
			),
		},
		{
			name:     "unlimited registry reports 0",
			registry: map[string]any{"maxStorage": "0", "includedStorage": "0"},
			check:    resource.TestCheckResourceAttr("data.kupe_plan.starter", "registry_max_storage", "0"),
		},
		{
			name: "no registry stays null",
			check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr("data.kupe_plan.starter", "registry_max_storage"),
				resource.TestCheckNoResourceAttr("data.kupe_plan.starter", "registry_included_storage"),
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			if tt.maxDatabases != nil {
				mock.plans["starter"]["maxDatabases"] = tt.maxDatabases
			}
			if tt.registry != nil {
				mock.plans["starter"]["registry"] = tt.registry
			}

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
							// The mock serves platformFee "29.00"; the
							// attribute stays null all the same (no platform
							// costs in Terraform, kupecloud/kupe-api#42).
							resource.TestCheckNoResourceAttr("data.kupe_plan.starter", "platform_fee"),
							resource.TestCheckResourceAttr("data.kupe_plan.starter", "max_clusters", "3"),
							tt.check,
						),
					},
				},
			})
		})
	}
}
