package provider

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// poolerConfig is the kupe_database.test config with a `postgres` attribute.
// An empty body leaves the attribute out.
func poolerConfig(mock *mockKupeAPI, postgres string) string {
	config := testAccDatabaseConfig(mock.url(), testDatabase{protection: boolPtr(false)})
	if postgres == "" {
		return config
	}
	return strings.Replace(config, `name = "orders-db"`, `name = "orders-db"`+"\n  postgres = "+postgres, 1)
}

// poolerStatus is a Running database status: Ready True, with PoolerReady as
// given.
func poolerStatus(poolerReady bool) map[string]any {
	status := "False"
	if poolerReady {
		status = "True"
	}
	return map[string]any{
		"phase": "Running",
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "Healthy"},
			map[string]any{"type": "PoolerReady", "status": status, "reason": "PoolerStarting"},
		},
	}
}

// lastDatabaseRequest checks the newest request the mock saw is this method
// with exactly this postgres body.
func lastDatabaseRequest(mock *mockKupeAPI, method string, want map[string]any) resource.TestCheckFunc {
	return testAccCheckDatabaseRequests(mock, func(reqs []mockRequest) error {
		if len(reqs) == 0 {
			return errors.New("no database request recorded")
		}
		last := reqs[len(reqs)-1]
		if last.method != method {
			return fmt.Errorf("last request is %s, want %s", last.method, method)
		}
		if len(last.body) != 1 {
			return fmt.Errorf("last request body %v carries more than postgres", last.body)
		}
		got, _ := last.body["postgres"].(map[string]any)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("last request postgres = %v, want %v", got, want)
		}
		return nil
	})
}

// TestAccDatabaseResource_Pooler walks the pooler through every change in
// place: turned on beside an existing database, a mode change, removal from
// the configuration (which keeps it), and turned off. Each PATCH carries only
// what changed under postgres.pooler and nothing of roles, extensions or
// databases, and an omitted `postgres` plans no change.
func TestAccDatabaseResource_Pooler(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	const addr = "kupe_database.test"
	steps := []resource.TestStep{
		{
			// Created without `postgres`: the API's pooler (off, defaults) is read, no drift.
			Config: poolerConfig(mock, ""),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "postgres.pooler.enabled", "false"),
				resource.TestCheckResourceAttr(addr, "postgres.pooler.pool_mode", "transaction"),
				resource.TestCheckResourceAttr(addr, "postgres.pooler.instances", "1"),
			),
		},
		{
			Config:           poolerConfig(mock, `{ pooler = { enabled = true } }`),
			ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "postgres.pooler.enabled", "true"),
				lastDatabaseRequest(mock, "PATCH", map[string]any{"pooler": map[string]any{"enabled": true}}),
			),
		},
		{
			Config:           poolerConfig(mock, `{ pooler = { enabled = true, pool_mode = "session" } }`),
			ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "postgres.pooler.pool_mode", "session"),
				lastDatabaseRequest(mock, "PATCH", map[string]any{"pooler": map[string]any{"poolMode": "session"}}),
			),
		},
		{
			Config:           poolerConfig(mock, `{ pooler = { enabled = true, pool_mode = "session", instances = 2 } }`),
			ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "postgres.pooler.instances", "2"),
				lastDatabaseRequest(mock, "PATCH", map[string]any{"pooler": map[string]any{"instances": float64(2)}}),
			),
		},
		{
			// `pooler` removed, then `postgres` removed: the setting stays.
			Config:           poolerConfig(mock, `{ roles = [{ name = "reporting" }] }`),
			ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "postgres.pooler.enabled", "true"),
				resource.TestCheckResourceAttr(addr, "postgres.pooler.pool_mode", "session"),
				lastDatabaseRequest(mock, "PATCH", map[string]any{"roles": []any{map[string]any{"name": "reporting"}}}),
			),
		},
		{
			Config:           poolerConfig(mock, ""),
			ConfigPlanChecks: expectAction(plancheck.ResourceActionNoop),
			Check:            resource.TestCheckResourceAttr(addr, "postgres.pooler.enabled", "true"),
		},
		{
			Config:           poolerConfig(mock, `{ pooler = { enabled = false } }`),
			ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "postgres.pooler.enabled", "false"),
				lastDatabaseRequest(mock, "PATCH", map[string]any{"pooler": map[string]any{"enabled": false}}),
			),
		},
		{
			// Import reads the pooler: the plan afterwards is empty.
			ResourceName:                         addr,
			ImportState:                          true,
			ImportStateVerify:                    true,
			ImportStateId:                        "orders-db",
			ImportStateVerifyIdentifierAttribute: "name",
			ImportStateVerifyIgnore:              []string{"timeouts"},
		},
		{Config: poolerConfig(mock, `{ pooler = { enabled = false } }`), PlanOnly: true},
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps:                    steps,
	})
}

// TestAccDatabaseResource_PoolerOnCreate: a configured pooler is sent on
// create, only the keys that are configured.
func TestAccDatabaseResource_PoolerOnCreate(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{{
			Config: poolerConfig(mock, `{ pooler = { enabled = true, pool_mode = "session" } }`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("kupe_database.test", "postgres.pooler.instances", "1"),
				testAccCheckDatabaseRequests(mock, func(reqs []mockRequest) error {
					got, _ := reqs[0].body["postgres"].(map[string]any)
					want := map[string]any{"pooler": map[string]any{"enabled": true, "poolMode": "session"}}
					if reqs[0].method != "POST" || fmt.Sprint(got) != fmt.Sprint(want) {
						return fmt.Errorf("create sent %s postgres %v, want %v", reqs[0].method, got, want)
					}
					return nil
				}),
			),
		}},
	})
}

// TestAccDatabaseResource_PoolerChangeWaits: an update that only changes the
// pooler waits until PoolerReady is True. Without the PoolerReady check in
// databaseConverged the first scripted status (Ready True) would end the wait
// and two scripted statuses would be left.
func TestAccDatabaseResource_PoolerChangeWaits(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: poolerConfig(mock, "")},
			{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.databaseStatusesOnPatch = []map[string]any{poolerStatus(false), poolerStatus(false), poolerStatus(true)}
					})
				},
				Config: poolerConfig(mock, `{ pooler = { enabled = true } }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kupe_database.test", "postgres.pooler.enabled", "true"),
					testAccCheckDatabaseStatusesPlayed(mock),
				),
			},
			{
				// Turned off: the condition is gone, and the apply does not wait for it.
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.databaseStatusesOnPatch = nil; m.databaseStatuses = nil })
				},
				// A short update timeout so a regression that waits fails fast.
				Config: strings.Replace(poolerConfig(mock, `{ pooler = { enabled = false } }`),
					`deletion_protection = false`, `deletion_protection = false
  timeouts = { update = "5s" }`, 1),
				Check: resource.TestCheckResourceAttr("kupe_database.test", "postgres.pooler.enabled", "false"),
			},
		},
	})
}

// TestAccDatabaseResource_PoolerChangeTimesOutUnserved: a pooler that never
// serves fails the update with the wait's timeout, naming the status.
func TestAccDatabaseResource_PoolerChangeTimesOutUnserved(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{Config: poolerConfig(mock, "")},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.poolerNotReady = true }) },
				Config: strings.Replace(poolerConfig(mock, `{ pooler = { enabled = true } }`),
					`deletion_protection = false`, `deletion_protection = false
  timeouts = { update = "1s" }`, 1),
				ExpectError: regexp.MustCompile(`(?i)timed out|timeout`),
			},
			{
				PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.poolerNotReady = false }) },
				Config:    poolerConfig(mock, `{ pooler = { enabled = true } }`),
			},
		},
	})
}

func TestAccDatabaseResource_PoolerUnavailable(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.poolerUnavailable = true
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{
			{
				Config:      poolerConfig(mock, `{ pooler = { enabled = true } }`),
				ExpectError: regexp.MustCompile(`DATABASE_POOLER_UNAVAILABLE`),
			},
			{Config: poolerConfig(mock, "")},
			{
				Config:      poolerConfig(mock, `{ pooler = { enabled = true } }`),
				ExpectError: regexp.MustCompile(`(?s)DATABASE_POOLER_UNAVAILABLE.*Connection pooling is not available on this platform`),
			},
			// The refused PATCH changed nothing: the pooler is still off.
			{Config: poolerConfig(mock, `{ pooler = { enabled = false } }`), PlanOnly: true},
			// Off is accepted while the platform does not offer the pooler.
			{Config: poolerConfig(mock, `{ pooler = { enabled = false } }`)},
		},
	})
}

func TestAccDatabaseResource_PoolerValidation(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{Config: poolerConfig(mock, `{ pooler = { enabled = true, pool_mode = "statement" } }`), PlanOnly: true, ExpectError: regexp.MustCompile(`pool_mode`)},
			{Config: poolerConfig(mock, `{ pooler = { enabled = true, instances = 3 } }`), PlanOnly: true, ExpectError: regexp.MustCompile(`instances`)},
			{Config: poolerConfig(mock, `{ pooler = { instances = 1 } }`), PlanOnly: true, ExpectError: regexp.MustCompile(`enabled`)},
		},
	})
}

func TestAccDatabaseDataSource_Pooler(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	shrinkPollInterval(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		CheckDestroy:             testAccCheckNoDatabases(mock),
		Steps: []resource.TestStep{{
			Config: poolerConfig(mock, `{ pooler = { enabled = true, instances = 2 } }`) +
				`data "kupe_database" "test" { name = kupe_database.test.name }`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kupe_database.test", "postgres.pooler.enabled", "true"),
				resource.TestCheckResourceAttr("data.kupe_database.test", "postgres.pooler.pool_mode", "transaction"),
				resource.TestCheckResourceAttr("data.kupe_database.test", "postgres.pooler.instances", "2"),
			),
		}},
	})
}

func TestPoolerChanges(t *testing.T) {
	pooler := func(enabled types.Bool, mode types.String, instances types.Int64) types.Object {
		return types.ObjectValueMust(databasePoolerAttrTypes, map[string]attr.Value{"enabled": enabled, "pool_mode": mode, "instances": instances})
	}
	off := pooler(types.BoolValue(false), types.StringValue("transaction"), types.Int64Value(1))
	on := pooler(types.BoolValue(true), types.StringValue("transaction"), types.Int64Value(1))
	unknownRest := pooler(types.BoolValue(true), types.StringUnknown(), types.Int64Unknown())
	null := types.ObjectNull(databasePoolerAttrTypes)
	if got := poolerChanges(on, on); got != nil {
		t.Errorf("unchanged pooler sends %+v", got)
	}
	if got := poolerChanges(null, off); got != nil {
		t.Errorf("a null plan sends %+v", got)
	}
	got := poolerChanges(off, on)
	if got == nil || got.Enabled == nil || *got.Enabled || got.PoolMode != "" || got.Instances != 0 {
		t.Errorf("turning off sends %+v, want only enabled=false", got)
	}
	if got := poolerChanges(unknownRest, null); got == nil || got.Enabled == nil || !*got.Enabled || got.PoolMode != "" || got.Instances != 0 {
		t.Errorf("create with unknown rest sends %+v, want only enabled=true", got)
	}
}

func TestDatabaseConvergedWaitsForPoolerOnlyWhileOn(t *testing.T) {
	on, off := true, false
	db := func(enabled *bool, conditions ...client.Condition) *client.Database {
		return &client.Database{
			Generation: 2,
			Postgres:   &client.DatabasePostgres{Pooler: &client.DatabasePooler{Enabled: enabled}},
			Status: &client.DatabaseStatus{
				Phase: "Running", ObservedGeneration: 2,
				Conditions: append([]client.Condition{{Type: "Ready", Status: "True"}}, conditions...),
			},
		}
	}
	tests := []struct {
		name string
		db   *client.Database
		want bool
	}{
		{"on and serving", db(&on, client.Condition{Type: "PoolerReady", Status: "True"}), true},
		{"on and starting", db(&on, client.Condition{Type: "PoolerReady", Status: "False"}), false},
		{"on and no condition yet", db(&on), false},
		{"off, condition removed", db(&off), true},
		{"no pooler in the response", &client.Database{Generation: 2, Status: db(&off).Status}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseConverged(tt.db); got != tt.want {
				t.Errorf("databaseConverged = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPoolerUnavailableDiagnostic(t *testing.T) {
	err := &client.APIError{StatusCode: 400, Code: client.CodeDatabasePoolerUnavailable, Message: "connection pooling is not available on this platform", Field: "spec.postgres.pooler"}
	detail := apiErrorDetail(err)
	for _, want := range []string{"DATABASE_POOLER_UNAVAILABLE", "Connection pooling is not available on this platform", "enabled = false"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %q", detail, want)
		}
	}
}
