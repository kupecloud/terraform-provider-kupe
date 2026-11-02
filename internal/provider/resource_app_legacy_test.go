package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// The pre-App provider inventory writes actual state with no App resources.
// Existing resource schemas/lifecycles remain the merged base's implementations.
type preAppProvider struct{ provider.Provider }

func (p *preAppProvider) Resources(ctx context.Context) []func() resource.Resource {
	resources := []func() resource.Resource{}
	for _, newResource := range p.Provider.Resources(ctx) {
		r := newResource()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "kupe"}, &meta)
		if meta.TypeName != "kupe_app" && meta.TypeName != "kupe_app_environment" {
			resources = append(resources, newResource)
		}
	}
	return resources
}

func TestAccAppAdditionPreservesExistingProviderState(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	legacy := map[string]func() (tfprotov6.ProviderServer, error){"kupe": providerserver.NewProtocol6WithError(&preAppProvider{Provider: New("pre-app")()})}
	config := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})
	acc.UnitTest(t, acc.TestCase{Steps: []acc.TestStep{
		{Config: config, ProtoV6ProviderFactories: legacy},
		{Config: config, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
	}})
}
