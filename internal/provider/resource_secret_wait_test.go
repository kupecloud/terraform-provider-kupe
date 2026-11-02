package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func secretScript(status map[string]any) []map[string]any {
	// Extra observations prevent the harness's post-apply refresh from
	// accidentally satisfying a test after a phase-only early return.
	return []map[string]any{status, status, status, status, status, status, {}}
}

func TestAccSecretResourceUpdateWait(t *testing.T) {
	shrinkPollInterval(t)
	synced := map[string]any{"cluster": "prod", "namespace": "app", "secretName": "safe", "phase": "Synced"}
	tests := []struct {
		name   string
		status map[string]any
	}{
		{"old Active generation", map[string]any{"phase": "Active", "observedGeneration": 1}},
		{"Active with a Pending target", map[string]any{"syncStatuses": []any{map[string]any{"cluster": "prod", "namespace": "app", "secretName": "safe", "phase": "Pending", "reason": "AwaitingCopy"}}}},
		{"Active while withdrawing", map[string]any{"syncStatuses": []any{synced, map[string]any{"cluster": "old", "namespace": "app", "phase": "Withdrawing"}}}},
		{"ProviderError is ridden out", map[string]any{"phase": "Degraded", "syncStatuses": []any{map[string]any{"cluster": "prod", "namespace": "app", "secretName": "safe", "phase": "Failed", "reason": "ProviderError"}}}},
		{"stale generation conflict is ridden out", map[string]any{"phase": "Degraded", "observedGeneration": 1, "syncStatuses": []any{map[string]any{"cluster": "prod", "namespace": "app", "secretName": "safe", "phase": "Failed", "reason": "TargetConflict"}}}},
		{"old name conflict after rename is ridden out", map[string]any{"phase": "Degraded", "syncStatuses": []any{map[string]any{"cluster": "prod", "namespace": "app", "secretName": "db-password", "phase": "Failed", "reason": "TargetConflict"}}}},
		{"removed target conflict is ridden out", map[string]any{"phase": "Degraded", "syncStatuses": []any{map[string]any{"cluster": "old", "namespace": "app", "secretName": "safe", "phase": "Failed", "reason": "TargetConflict"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			acc.UnitTest(t, acc.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
				Steps: []acc.TestStep{
					{Config: secretConfig(mock.url(), secretTargetConfig(""), "")},
					{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.secretStatusesOnPatch = secretScript(tt.status) }) },
						Config: secretConfig(mock.url(), `[{cluster="prod",namespace="app",secret_name="safe",keys=["password"]}]`, ""),
						Check: acc.ComposeAggregateTestCheckFunc(
							acc.TestCheckResourceAttr(secretAddr, "sync.0.secret_name", "safe"),
							acc.TestCheckResourceAttr(secretAddr, "sync_status.0.phase", "Synced"),
							func(*terraform.State) error {
								mock.mu.Lock()
								defer mock.mu.Unlock()
								if len(mock.secretStatuses) != 1 {
									return fmt.Errorf("wait returned with %d scripted statuses left", len(mock.secretStatuses))
								}
								return nil
							}),
					},
				},
			})
		})
	}
}

func TestAccSecretResourceCreateFailureTracksAndTaints(t *testing.T) {
	shrinkPollInterval(t)
	for _, tt := range []struct {
		name   string
		status map[string]any
		want   string
	}{
		{"conflict", map[string]any{"phase": "Degraded", "syncStatuses": []any{map[string]any{"cluster": "prod", "namespace": "app", "secretName": "db-password", "phase": "Failed", "reason": "TargetConflict"}}}, `(?s)prod/app.*TargetConflict.*marked tainted`},
		{"deleting", map[string]any{"phase": "Deleting", "observedGeneration": 0}, `(?s)being deleted.*create did not complete`},
		{"timeout", map[string]any{"phase": "Active", "observedGeneration": 0}, `(?s)secret create timed out.*marked tainted`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			mock.secretStatuses = []map[string]any{tt.status}
			config := secretConfig(mock.url(), secretTargetConfig(""), `timeouts = { create = "1s" }`)
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: config, ExpectError: regexp.MustCompile(tt.want)},
				{PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						if tt.name != "timeout" && m.secretGets > 4 {
							t.Errorf("terminal wait kept polling (%d GETs)", m.secretGets)
						}
						m.secretStatuses = nil
					})
				}, Config: config,
					ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(secretAddr, plancheck.ResourceActionDestroyBeforeCreate)}},
					Check:            acc.TestCheckResourceAttr(secretAddr, "phase", "Active")},
			}})
		})
	}
}

func TestAccSecretResourceUpdateFailureKeepsDesiredState(t *testing.T) {
	shrinkPollInterval(t)
	for _, tt := range []struct {
		name   string
		status map[string]any
		want   string
	}{
		{"timeout", map[string]any{"phase": "Active", "observedGeneration": 1}, `(?s)secret update timed out.*` + wrappedText(changeRecordedNotice)},
		{"conflict", map[string]any{"phase": "Degraded", "syncStatuses": []any{map[string]any{"cluster": "prod", "namespace": "app", "secretName": "db-password", "phase": "Failed", "reason": "TargetConflict"}}}, `(?s)prod/app.*TargetConflict.*` + wrappedText(changeRecordedNotice)},
		{"deleting", map[string]any{"phase": "Deleting", "observedGeneration": 1}, `(?s)being deleted.*update did not complete`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			config := secretConfig(mock.url(), secretTargetConfig(`["password"]`), `timeouts = { update = "1s" }`)
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: secretConfig(mock.url(), secretTargetConfig(""), `timeouts = { update = "1s" }`)},
				{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.secretStatusesOnPatch = []map[string]any{tt.status} }) }, Config: config, ExpectError: regexp.MustCompile(tt.want)},
				{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.secretStatuses = nil; m.secretStatusesOnPatch = nil }) }, Config: config,
					ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
					Check: func(*terraform.State) error {
						mock.mu.Lock()
						defer mock.mu.Unlock()
						if len(mock.secretRequests) != 2 {
							return fmt.Errorf("accepted update was re-sent: %d writes", len(mock.secretRequests))
						}
						return nil
					}},
			}})
		})
	}
}

func TestAccSecretResourceETagRetry(t *testing.T) {
	for _, tt := range []struct {
		name      string
		change    func(map[string]any)
		wantError bool
	}{
		{"status only", func(s map[string]any) { s["status"] = map[string]any{"phase": "Active"} }, false},
		{"keys ordering only", func(s map[string]any) {
			target := s["sync"].([]any)[0].(map[string]any)
			target["keys"] = []any{"username", "password"}
		}, false},
		{"another key changed", func(s map[string]any) {
			target := s["sync"].([]any)[0].(map[string]any)
			target["keys"] = []any{"other"}
		}, true},
		{"another target changed", func(s map[string]any) { target := s["sync"].([]any)[0].(map[string]any); target["namespace"] = "other" }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			step := acc.TestStep{
				PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) { m.secretBeforePatch = []func(map[string]any){tt.change} })
				},
				Config: secretConfig(mock.url(), secretTargetConfig(`["password"]`), ""),
			}
			if tt.wantError {
				step.ExpectError = regexp.MustCompile(`(?s)secret changed outside this apply.*` + wrappedText("sync now differs"))
			}
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: secretConfig(mock.url(), secretTargetConfig(`["password","username"]`), "")}, step,
			}})
			mock.mu.Lock()
			defer mock.mu.Unlock()
			want := 3
			if tt.wantError {
				want = 2
			}
			if len(mock.secretRequests) != want {
				t.Fatalf("writes = %d, want %d", len(mock.secretRequests), want)
			}
			if !tt.wantError && mock.secretRequests[1].ifMatch == mock.secretRequests[2].ifMatch {
				t.Error("retry did not use fresh ETag")
			}
		})
	}
}

// Direct lifecycle calls expose state-before-wait and warning diagnostics,
// which the acceptance harness does not surface as checks.
func TestSecretCreateRecordsStateBeforeFirstPoll(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.secretGetErrorStatus = http.StatusForbidden
	r := NewSecretResource().(*SecretResource)
	r.client = client.New(mock.url(), "acme", "kupe_test_key")
	s := testResourceSchema(t, r)
	raw := testPlanValue(t, s, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "db-password"), "secret_path": tftypes.NewValue(tftypes.String, "app/config")})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: raw.Copy()}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s.Schema, Raw: raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected first poll refusal")
	}
	var state SecretResourceModel
	if d := resp.State.Get(context.Background(), &state); d.HasError() {
		t.Fatalf("accepted state missing: %v", d)
	}
	if state.Name.ValueString() != "db-password" || state.Phase.ValueString() != "Pending" {
		t.Error("POST result not saved before failed GET")
	}
}

func TestSecretDeleteWaitAndTimeoutWarning(t *testing.T) {
	shrinkPollInterval(t)
	for _, lag := range []int{3, -1} {
		t.Run(fmt.Sprintf("lag %d", lag), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			c := client.New(mock.url(), "acme", "kupe_test_key")
			if _, _, err := c.CreateSecret(context.Background(), client.CreateSecretRequest{Name: "db-password", SecretPath: "app/config"}); err != nil {
				t.Fatal(err)
			}
			mock.secretDeleteLag = lag
			r := NewSecretResource().(*SecretResource)
			r.client = c
			s := testResourceSchema(t, r)
			timeoutType := s.Schema.Type().TerraformType(context.Background()).(tftypes.Object).AttributeTypes["timeouts"].(tftypes.Object)
			timeoutValues := map[string]tftypes.Value{}
			for key, typ := range timeoutType.AttributeTypes {
				timeoutValues[key] = tftypes.NewValue(typ, nil)
			}
			timeoutValues["delete"] = tftypes.NewValue(tftypes.String, "80ms")
			raw := testPlanValue(t, s, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "db-password"), "timeouts": tftypes.NewValue(timeoutType, timeoutValues)})
			var resp resource.DeleteResponse
			r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s.Schema, Raw: raw}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("delete: %v", resp.Diagnostics)
			}
			mock.mu.Lock()
			defer mock.mu.Unlock()
			if lag > 0 {
				if len(resp.Diagnostics) != 0 || len(mock.secrets) != 0 || mock.secretGets < 4 {
					t.Error("delete did not wait for 404")
				}
			} else {
				warn := strings.Join(warnings(resp.Diagnostics), " ")
				if !strings.Contains(warn, "name stays reserved") || !strings.Contains(warn, "background") || len(mock.secrets) != 1 {
					t.Error("timeout did not retain withdrawal and warn")
				}
			}
		})
	}
}

func TestAccSecretResourceDeleteWaitsForWithdrawal(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := secretConfig(mock.url(), secretTargetConfig(""), `timeouts={delete="1s"}`)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config},
		{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.secretDeleteLag = 3; m.secretGets = 0 }) }, Config: config, Destroy: true},
	}})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.secrets) != 0 || mock.secretGets < 4 {
		t.Error("destroy returned before withdrawal and 404")
	}
}
