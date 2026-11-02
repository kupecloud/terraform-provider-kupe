package provider

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
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
					// Create waits for this generation at every target.
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

func TestSecretDelivered(t *testing.T) {
	targets := []client.SyncTarget{{Cluster: "prod", Namespace: "app"}, {Cluster: "prod", Namespace: "api", SecretName: "credentials"}}
	synced := []client.SecretTargetStatus{
		{Cluster: "prod", Namespace: "app", SecretName: "config", Phase: "Synced"},
		{Cluster: "prod", Namespace: "api", SecretName: "credentials", Phase: "Synced"},
	}
	tests := []struct {
		name   string
		mutate func(*client.Secret)
		want   bool
	}{
		{"all Synced", func(*client.Secret) {}, true},
		{"stale generation", func(s *client.Secret) { s.Status.ObservedGeneration = 1 }, false},
		{"zero generation cannot confirm delivery", func(s *client.Secret) { s.Generation = 0; s.Status.ObservedGeneration = 0 }, false},
		{"negative generation cannot confirm delivery", func(s *client.Secret) { s.Generation = -1; s.Status.ObservedGeneration = -1 }, false},
		{"one target Pending", func(s *client.Secret) { s.Status.SyncStatuses[1].Phase = "Pending" }, false},
		{"missing target", func(s *client.Secret) { s.Status.SyncStatuses = s.Status.SyncStatuses[:1] }, false},
		{"duplicate status cannot hide missing target", func(s *client.Secret) { s.Status.SyncStatuses[1] = s.Status.SyncStatuses[0] }, false},
		{"Withdrawing entry", func(s *client.Secret) {
			s.Status.SyncStatuses = append(s.Status.SyncStatuses, client.SecretTargetStatus{Phase: "Withdrawing"})
		}, false},
		{"old effective name", func(s *client.Secret) { s.Status.SyncStatuses[1].SecretName = "old" }, false},
		{"zero targets", func(s *client.Secret) { s.Sync = nil; s.Status.SyncStatuses = nil }, true},
		{"zero targets not observed", func(s *client.Secret) { s.Sync = nil; s.Status.ObservedGeneration = 1 }, false},
		{"zero targets with zero generation", func(s *client.Secret) {
			s.Sync = nil
			s.Status.SyncStatuses = nil
			s.Generation = 0
			s.Status.ObservedGeneration = 0
		}, false},
		{"Degraded", func(s *client.Secret) { s.Status.Phase = "Degraded" }, false},
		{"Deleting", func(s *client.Secret) { s.Status.Phase = "Deleting" }, false},
		{"no status", func(s *client.Secret) { s.Status = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &client.Secret{Name: "config", Generation: 2, Sync: slices.Clone(targets),
				Status: &client.SecretStatus{Phase: "Active", ObservedGeneration: 2, SyncStatuses: slices.Clone(synced)}}
			tt.mutate(s)
			if got := secretDelivered(s); got != tt.want {
				t.Errorf("delivered = %v, want %v", got, tt.want)
			}
		})
	}
	if secretDelivered(nil) {
		t.Error("nil secret delivered")
	}
}

// Missing fields decode to zero. Equality of those defaults proves neither
// current delivery nor a current conflict, including for a zero-target secret.
func TestSecretMissingGenerationCannotConfirmDeliveryOrConflict(t *testing.T) {
	for _, metadata := range []struct{ name, generation, observed string }{
		{"missing", "", ""},
		{"explicit zero", `"generation":0,`, `"observedGeneration":0,`},
		{"negative", `"generation":-1,`, `"observedGeneration":-1,`},
	} {
		for _, noTargets := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, no targets %v", metadata.name, noTargets), func(t *testing.T) {
				targets := `[{"cluster":"prod","namespace":"app"}]`
				if noTargets {
					targets = `[]`
				}
				wire := fmt.Sprintf(`{%s"name":"config","sync":%s,"status":{%s"phase":"Active","syncStatuses":[{"cluster":"prod","namespace":"app","secretName":"config","phase":"Synced"}]}}`, metadata.generation, targets, metadata.observed)
				var s client.Secret
				if err := json.Unmarshal([]byte(wire), &s); err != nil {
					t.Fatal(err)
				}
				if secretDelivered(&s) {
					t.Error("invalid or absent generation confirmed delivery")
				}
				s.Status.SyncStatuses[0].Phase = "Failed"
				s.Status.SyncStatuses[0].Reason = "TargetConflict"
				if _, failed := secretKind.ready.failed(&s); failed {
					t.Error("invalid or absent generation made an unproven conflict terminal")
				}
				s.Status.Phase = "Deleting"
				if _, failed := secretKind.ready.failed(&s); !failed {
					t.Error("Deleting must remain terminal without valid generation metadata")
				}
			})
		}
	}
}

func TestSecretTerminalFailure(t *testing.T) {
	for _, tt := range []struct {
		name, phase, reason, statusName string
		observed                        int64
		current, want                   bool
	}{
		{"current conflict", "Degraded", "TargetConflict", "new", 2, true, true},
		{"stale generation", "Degraded", "TargetConflict", "new", 1, true, false},
		{"old name after rename", "Degraded", "TargetConflict", "old", 2, true, false},
		{"removed target", "Degraded", "TargetConflict", "new", 2, false, false},
		{"provider failure retries", "Degraded", "ProviderError", "new", 2, true, false},
		{"deleting ignores generation", "Deleting", "", "old", 1, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &client.Secret{Name: "config", Generation: 2, Status: &client.SecretStatus{Phase: tt.phase, ObservedGeneration: tt.observed,
				SyncStatuses: []client.SecretTargetStatus{{Cluster: "prod", Namespace: "app", SecretName: tt.statusName, Phase: "Failed", Reason: tt.reason}}}}
			if tt.current {
				s.Sync = []client.SyncTarget{{Cluster: "prod", Namespace: "app", SecretName: "new"}}
			}
			if grace, got := secretKind.ready.failed(s); got != tt.want || grace != 0 {
				t.Errorf("terminal = %v, grace %s; want %v, immediate", got, grace, tt.want)
			}
		})
	}
}

// Response-only refresh tokens and timestamps must not create perpetual
// refresh drift. This feeds the wire representation through the real client
// model and compares the full mapped status in both response orders.
func TestSecretStatusStableAndMetadataOnly(t *testing.T) {
	decode := func(token, at string, reverse bool) *client.Secret {
		t.Helper()
		entries := []string{
			fmt.Sprintf(`{"cluster":"z","namespace":"a","secretName":"config","phase":"Synced","reason":"Delivered","deliveredRefresh":%q,"lastDeliveredAt":%q}`, token, at),
			`{"cluster":"a","namespace":"z","secretName":"old","phase":"Withdrawing","reason":"Withdrawing"}`,
			`{"cluster":"a","namespace":"a","secretName":"config","phase":"Pending","reason":"AwaitingCopy"}`,
		}
		if reverse {
			slices.Reverse(entries)
		}
		var s client.Secret
		if err := json.Unmarshal([]byte(`{"name":"config","status":{"phase":"Active","syncStatuses":[`+strings.Join(entries, ",")+`]}}`), &s); err != nil {
			t.Fatal(err)
		}
		return &s
	}
	var a, b SecretResourceModel
	mapSecretToState(decode("refresh-one", "2026-01-01", false), "same", &a)
	mapSecretToState(decode("refresh-two", "2026-02-01", true), "same", &b)
	if !a.SyncStatus.Equal(b.SyncStatus) {
		t.Error("status changed with response order or delivery metadata")
	}
	first := a.SyncStatus.Elements()[0].(types.Object).Attributes()
	if first["cluster"] != types.StringValue("a") || first["namespace"] != types.StringValue("a") {
		t.Error("status not sorted by cluster and namespace")
	}
	if strings.Contains(a.SyncStatus.String(), "refresh-one") || strings.Contains(a.SyncStatus.String(), "2026-01-01") {
		t.Error("delivery metadata leaked into state")
	}
	for _, attr := range []string{"delivered_refresh", "last_delivered_at", "observed_refresh"} {
		if _, exists := secretTargetStatusType.AttrTypes[attr]; exists {
			t.Errorf("status schema exposes %s", attr)
		}
	}
}

func TestSecretSyncNullAndEmpty(t *testing.T) {
	for _, value := range []types.List{types.ListNull(types.ObjectType{AttrTypes: syncTargetAttrTypes}), types.ListValueMust(types.ObjectType{AttrTypes: syncTargetAttrTypes}, []attr.Value{})} {
		state := SecretResourceModel{Sync: value}
		mapSecretToState(&client.Secret{Sync: []client.SyncTarget{}}, "", &state)
		if !state.Sync.Equal(value) {
			t.Errorf("sync = %s, want %s", state.Sync, value)
		}
		got := extractSyncTargets(value)
		if (got == nil) != value.IsNull() {
			t.Errorf("wire nil = %v, config null = %v", got == nil, value.IsNull())
		}
	}
}

const secretAddr = "kupe_secret.test"

func secretConfig(host, sync, extra string) string {
	if sync != "" {
		sync = "sync = " + sync
	}
	return testAccProviderConfig(host) + fmt.Sprintf(`
resource "kupe_secret" "test" {
  name = "db-password"
  secret_path = "production/db-password"
  %s
  %s
}
`, sync, extra)
}

func secretTargetConfig(keys string) string {
	if keys != "" {
		keys = ", keys = " + keys
	}
	return `[{cluster = "prod", namespace = "app"` + keys + `}]`
}

func checkSecretWrites(mock *mockKupeAPI, seen *int, method string, keys []string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		requests := mock.secretRequests[*seen:]
		*seen = len(mock.secretRequests)
		if method == "" {
			if len(requests) != 0 {
				return fmt.Errorf("unchanged plan sent %d writes", len(requests))
			}
			return nil
		}
		if len(requests) != 1 || requests[0].method != method {
			return fmt.Errorf("got %d writes, want one %s", len(requests), method)
		}
		if method == "PATCH" && requests[0].ifMatch == "" {
			return fmt.Errorf("PATCH omitted If-Match")
		}
		target := requests[0].body["sync"].([]any)[0].(map[string]any)
		got, exists := target["keys"]
		if keys == nil {
			if exists {
				return fmt.Errorf("omitted keys sent on wire")
			}
			return nil
		}
		var values []string
		if exists {
			for _, value := range got.([]any) {
				values = append(values, value.(string))
			}
		}
		if !slices.Equal(values, keys) {
			return fmt.Errorf("keys = %v, want %v", values, keys)
		}
		return nil
	}
}

func TestAccSecretResourceKeys(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	seen := 0
	step := func(keys, method string, want []string) resource.TestStep {
		return resource.TestStep{Config: secretConfig(mock.url(), secretTargetConfig(keys), ""),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(secretAddr, plancheck.ResourceActionUpdate)}},
			Check:            checkSecretWrites(mock, &seen, method, want)}
	}
	add := step(`["username", "password"]`, "PATCH", []string{"password", "username"})
	reorder := step(`["password", "username"]`, "", nil)
	reorder.ConfigPlanChecks = resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		Steps: []resource.TestStep{
			{Config: secretConfig(mock.url(), secretTargetConfig(""), ""), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(secretAddr, "sync.0.keys.#"), checkSecretWrites(mock, &seen, "POST", nil))},
			{Config: secretConfig(mock.url(), secretTargetConfig(""), ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: secretConfig(mock.url(), secretTargetConfig("null"), ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: resource.TestCheckNoResourceAttr(secretAddr, "sync.0.keys.#")},
			add, reorder,
			{ResourceName: secretAddr, ImportState: true, ImportStateVerify: true, ImportStateId: "db-password", ImportStateVerifyIdentifierAttribute: "name"},
			step(`["password"]`, "PATCH", []string{"password"}),
			step("", "PATCH", nil),
		},
	})
}

func TestAccSecretResourceMultipleTargets(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	targets := `[{cluster="z",namespace="app",keys=["password"]},{cluster="a",namespace="z"},{cluster="a",namespace="a",keys=["username"]}]`
	reordered := `[{cluster="a",namespace="a",keys=["username"]},{cluster="z",namespace="app",keys=["password"]},{cluster="a",namespace="z"}]`
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: secretConfig(mock.url(), targets, ""), Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(secretAddr, "sync.0.cluster", "z"),
			resource.TestCheckResourceAttr(secretAddr, "sync_status.0.cluster", "a"),
			resource.TestCheckResourceAttr(secretAddr, "sync_status.0.namespace", "a"),
			resource.TestCheckResourceAttr(secretAddr, "sync_status.1.namespace", "z"),
			resource.TestCheckResourceAttr(secretAddr, "sync_status.2.cluster", "z"),
		)},
		{Config: secretConfig(mock.url(), reordered, ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(secretAddr, plancheck.ResourceActionUpdate)}}, Check: resource.TestCheckResourceAttr(secretAddr, "sync.0.namespace", "a")},
		{ResourceName: secretAddr, ImportState: true, ImportStateVerify: true, ImportStateId: "db-password", ImportStateVerifyIdentifierAttribute: "name"},
		{Config: secretConfig(mock.url(), "[]", ""), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr(secretAddr, "sync.#", "0"), resource.TestCheckResourceAttr(secretAddr, "sync_status.#", "0"))},
	}})
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
