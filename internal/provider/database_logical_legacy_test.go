package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

type preLogicalDatabaseProvider struct{ provider.Provider }

func (*preLogicalDatabaseProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource {
		r := NewDatabaseResource().(*DatabaseResource)
		kind := *r.kind
		kind.mapToState = func(db *client.Database, etag string, m *DatabaseResourceModel) {
			mapDatabaseToState(db, etag, m)
			if isSet(m.Postgres) {
				values := m.Postgres.Attributes()
				delete(values, "databases")
				m.Postgres = types.ObjectValueMust(map[string]attr.Type{"roles": types.SetType{ElemType: postgresNameType}, "extensions": types.SetType{ElemType: postgresNameType}}, values)
			}
		}
		r.kind = &kind
		return &preLogicalDatabaseResource{r}
	}}
}

type preLogicalDatabaseResource struct{ *DatabaseResource }

func (r *preLogicalDatabaseResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.DatabaseResource.Schema(ctx, req, resp)
	p := resp.Schema.Attributes["postgres"].(schema.SingleNestedAttribute)
	delete(p.Attributes, "databases")
	resp.Schema.Attributes["postgres"] = p
}
func TestAccDatabaseResource_StateWithoutDatabasesHasNoDiff(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	old := map[string]func() (tfprotov6.ProviderServer, error){"kupe": providerserver.NewProtocol6WithError(&preLogicalDatabaseProvider{New("pre-logical")()})}
	configured := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})
	configured = strings.Replace(configured, `name = "orders-db"`, `name = "orders-db"`+"\n postgres={roles=[{name=\"reporting\"}]} ", 1)
	acc.UnitTest(t, acc.TestCase{Steps: []acc.TestStep{
		{Config: configured, ProtoV6ProviderFactories: old, Check: acc.TestCheckNoResourceAttr("kupe_database.test", "postgres.databases.#")},
		{Config: configured, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: acc.TestCheckResourceAttr("kupe_database.test", "postgres.databases.0.name", "orders_db")},
	}})
	if len(mock.databaseRequests) != 1 {
		t.Fatalf("legacy refresh wrote settings: %v", mock.databaseRequests)
	}
}
