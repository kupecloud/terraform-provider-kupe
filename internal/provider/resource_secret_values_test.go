package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const secretValueSentinel = "test-secret-sentinel-must-not-appear"

func secretValuesConfig(host string, version int64, extra string) string {
	return `variable "kupe_secret_values" {
  type = map(string)
  sensitive = true
  ephemeral = true
}
` + secretConfig(host, secretTargetConfig(""), fmt.Sprintf("values_wo = var.kupe_secret_values\nvalues_wo_version = %d\n%s", version, extra))
}

func setSecretTestValues(t *testing.T) {
	t.Helper()
	t.Setenv("TF_VAR_kupe_secret_values", `{"password":"`+secretValueSentinel+`"}`)
}

// Both checks inspect the real CLI's show -json results, including the
// saved plan's configuration and variables, not only resource attributes.
type secretNoValuesPlan struct{}

func (secretNoValuesPlan) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	data, err := json.Marshal(req.Plan)
	if err != nil {
		resp.Error = err
		return
	}
	if strings.Contains(string(data), secretValueSentinel) {
		resp.Error = fmt.Errorf("secret sentinel found in saved plan JSON")
	}
}

type secretNoValuesState struct{}

func (secretNoValuesState) CheckState(_ context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
	data, err := json.Marshal(req.State)
	if err != nil {
		resp.Error = err
		return
	}
	if strings.Contains(string(data), secretValueSentinel) {
		resp.Error = fmt.Errorf("secret sentinel found in state JSON")
	}
}

func checkVaultWrites(mock *mockKupeAPI, want int, match string) acc.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.vaultRequests) != want {
			return fmt.Errorf("PUT count %d, want %d", len(mock.vaultRequests), want)
		}
		if want > 0 && mock.vaultRequests[want-1].ifMatch != match {
			return fmt.Errorf("last If-Match %q, want %q", mock.vaultRequests[want-1].ifMatch, match)
		}
		if mock.vaultReadValues {
			return fmt.Errorf("provider requested values on read")
		}
		return nil
	}
}

// Compare contents without ever rendering synthetic secret values in a
// failing check. Key names, membership and counts are sufficient context.
func secretValuesMapEqual(got, want map[string]string) error {
	if len(got) != len(want) {
		return fmt.Errorf("value map key count differs: got %d, want %d", len(got), len(want))
	}
	for key, value := range want {
		stored, present := got[key]
		if !present {
			return fmt.Errorf("configured key %q is absent", key)
		}
		if stored != value {
			return fmt.Errorf("stored value differs for configured key %q", key)
		}
	}
	return nil
}

func checkVaultValues(mock *mockKupeAPI, request int, want map[string]string) acc.TestCheckFunc {
	return func(*terraform.State) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if request >= len(mock.vaultRequests) {
			return fmt.Errorf("expected value request is absent")
		}
		if err := secretValuesMapEqual(mock.vaultRequests[request].values, want); err != nil {
			return fmt.Errorf("captured replacement: %w", err)
		}
		entry := mock.vaultEntries["production/db-password"]
		if entry == nil {
			return fmt.Errorf("stored entry is absent")
		}
		if err := secretValuesMapEqual(entry.values, want); err != nil {
			return fmt.Errorf("stored replacement: %w", err)
		}
		if mock.vaultReadValues {
			return fmt.Errorf("provider requested values on read")
		}
		return nil
	}
}

func TestAccSecretValuesLifecycle(t *testing.T) {
	createdValues := map[string]string{"password": secretValueSentinel, "username": "test-synthetic-username"}
	replacedValues := map[string]string{"password": secretValueSentinel + "-rotated"}
	t.Setenv("TF_VAR_kupe_secret_values", `{"password":"`+secretValueSentinel+`","username":"test-synthetic-username"}`)
	mock := newMockKupeAPI()
	defer mock.close()
	step := func(version int64, writes int, match string, want map[string]string) acc.TestStep {
		return acc.TestStep{Config: secretValuesConfig(mock.url(), version, ""),
			ConfigPlanChecks:  acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{secretNoValuesPlan{}}},
			ConfigStateChecks: []statecheck.StateCheck{secretNoValuesState{}},
			Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, writes, match), checkVaultValues(mock, writes-1, want), acc.TestCheckNoResourceAttr(secretAddr, "values_wo.%"),
				acc.TestCheckResourceAttr(secretAddr, "values_wo_version", fmt.Sprint(version)), acc.TestCheckResourceAttr(secretAddr, "source_version", fmt.Sprint(writes)))}
	}
	replace := step(2, 2, `"1"`, replacedValues)
	replace.PreConfig = func() { t.Setenv("TF_VAR_kupe_secret_values", `{"password":"`+secretValueSentinel+`-rotated"}`) }
	unchanged := step(2, 2, `"1"`, replacedValues)
	unchanged.PreConfig = func() { t.Setenv("TF_VAR_kupe_secret_values", `{"password":"different-test-value"}`) }
	unchanged.ConfigPlanChecks.PreApply = append(unchanged.ConfigPlanChecks.PreApply, plancheck.ExpectEmptyPlan())
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		step(1, 1, "", createdValues), replace, unchanged,
		{Config: secretConfig(mock.url(), secretTargetConfig(""), ""), Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 2, `"1"`), checkVaultValues(mock, 1, replacedValues), acc.TestCheckNoResourceAttr(secretAddr, "values_wo_version"))},
	}})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.secrets) != 0 || mock.vaultEntries["production/db-password"] == nil || mock.vaultEntries["production/db-password"].version != 2 {
		t.Error("destroy did not keep stored values")
	}
	if entry := mock.vaultEntries["production/db-password"]; entry != nil {
		if err := secretValuesMapEqual(entry.values, replacedValues); err != nil {
			t.Errorf("destroy changed the confirmed map: %s", err)
		}
	}
	if mock.vaultRequests[0].ifNoneMatch != "*" {
		t.Error("create did not send If-None-Match")
	}
}

func TestAccSecretValuesCreateExistingRefused(t *testing.T) {
	setSecretTestValues(t)
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("soft deleted %v", deleted), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			mock.setState(func(m *mockKupeAPI) {
				m.vaultEntries = map[string]*mockVaultEntry{"production/db-password": {version: 4, deleted: deleted}}
			})
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{{Config: secretValuesConfig(mock.url(), 1, ""), ExpectError: regexp.MustCompile(wrappedText("apply this resource once without values_wo"))}}})
			mock.mu.Lock()
			changed := len(mock.secrets) != 0 || len(mock.vaultRequests) != 1 || mock.vaultEntries["production/db-password"].version != 4
			mock.mu.Unlock()
			if changed {
				t.Error("create refusal changed existing entry or created definition")
			}
		})
	}
}

func TestAccSecretValuesImportThenWrite(t *testing.T) {
	setSecretTestValues(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) {
		m.vaultEntries = map[string]*mockVaultEntry{"production/db-password": {version: 7}}
	})
	// A persisted command import must start with an unmanaged address. Seed
	// only the remote definition, then retain the imported state for the write.
	_, _, err := client.New(mock.url(), "acme", "test-token").CreateSecret(context.Background(), client.CreateSecretRequest{
		Name: "db-password", SecretPath: "production/db-password", Sync: []client.SyncTarget{{Cluster: "prod", Namespace: "app"}},
	})
	if err != nil {
		t.Fatal("could not seed the remote definition for import")
	}
	importConfig := `terraform {
  required_providers {
    kupe = { source = "kupecloud/kupe" }
  }
}
` + secretConfig(mock.url(), secretTargetConfig(""), "")
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		// Initialise the CLI provider selection without managing the seeded
		// definition; persisted command import reuses this working directory.
		{Config: testAccProviderConfig(mock.url()), Check: checkVaultWrites(mock, 0, "")},
		{Config: importConfig, ResourceName: secretAddr, ImportState: true, ImportStatePersist: true, ImportStateId: "db-password",
			ImportStateCheck: func(states []*terraform.InstanceState) error {
				if len(states) != 1 || states[0].Attributes["name"] != "db-password" || states[0].Attributes["source_version"] != "7" {
					return fmt.Errorf("import did not read the remote name and source metadata")
				}
				if states[0].Attributes["values_wo_version"] != "" {
					return fmt.Errorf("import assigned a write-only change counter")
				}
				return checkVaultWrites(mock, 0, "")(nil)
			}},
		{Config: secretValuesConfig(mock.url(), 1, ""), Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 1, `"7"`), acc.TestCheckResourceAttr(secretAddr, "source_version", "8"))},
	}})
}

type secretPlanAction func(plancheck.CheckPlanRequest) error

func (f secretPlanAction) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	resp.Error = f(req)
}

func priorSecretCounter(req plancheck.CheckPlanRequest, want string) error {
	if req.Plan.PriorState == nil || req.Plan.PriorState.Values == nil || req.Plan.PriorState.Values.RootModule == nil {
		return fmt.Errorf("missing prior state in saved plan")
	}
	for _, r := range req.Plan.PriorState.Values.RootModule.Resources {
		if r.Address == secretAddr && fmt.Sprint(r.AttributeValues["values_wo_version"]) == want {
			return nil
		}
	}
	return fmt.Errorf("saved plan prior state lost confirmed values_wo_version %s", want)
}

func TestAccSecretValuesSavedPlanConflict(t *testing.T) {
	setSecretTestValues(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mutateAfterSavedPlan := secretPlanAction(func(req plancheck.CheckPlanRequest) error {
		if err := priorSecretCounter(req, "1"); err != nil {
			return err
		}
		for _, r := range req.Plan.PriorState.Values.RootModule.Resources {
			if r.Address == secretAddr && fmt.Sprint(r.AttributeValues["source_version"]) != "1" {
				return fmt.Errorf("plan did not see source version 1")
			}
		}
		mock.setState(func(m *mockKupeAPI) { m.vaultEntries["production/db-password"].version = 2 })
		return nil
	})
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: secretValuesConfig(mock.url(), 1, "")},
		{Config: secretValuesConfig(mock.url(), 2, ""), ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{secretNoValuesPlan{}, mutateAfterSavedPlan}}, ExpectError: regexp.MustCompile(wrappedText("planned against version 1"))},
		{Config: secretValuesConfig(mock.url(), 2, ""), ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{secretPlanAction(func(req plancheck.CheckPlanRequest) error { return priorSecretCounter(req, "1") })}},
			Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 3, `"2"`), acc.TestCheckResourceAttr(secretAddr, "values_wo_version", "2"))},
	}})
	mock.mu.Lock()
	match := mock.vaultRequests[1].ifMatch
	mock.mu.Unlock()
	if match != `"1"` {
		t.Error("saved-plan apply rebased its source version and overwrote a concurrent write")
	}
}

func TestAccSecretValuesUnconfirmedRetriesOnlyOnNextApply(t *testing.T) {
	setSecretTestValues(t)
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprintf("disconnect %v", disconnect), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: secretValuesConfig(mock.url(), 1, "")},
				{PreConfig: func() {
					mock.setState(func(m *mockKupeAPI) {
						m.vaultFaults = []mockVaultFault{{status: 503, code: client.CodeSecretWriteUnconfirmed, disconnect: disconnect, commit: true}}
					})
				},
					Config: secretValuesConfig(mock.url(), 2, ""), ExpectError: regexp.MustCompile(wrappedText("the write may or may not have been saved"))},
				{Config: secretValuesConfig(mock.url(), 2, ""), ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{secretPlanAction(func(req plancheck.CheckPlanRequest) error {
					if err := priorSecretCounter(req, "1"); err != nil {
						return err
					}
					return checkVaultWrites(mock, 2, `"1"`)(nil)
				})}}, Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 3, `"2"`), acc.TestCheckResourceAttr(secretAddr, "values_wo_version", "2"), acc.TestCheckResourceAttr(secretAddr, "source_version", "3"))},
			}})
		})
	}
}

func TestAccSecretValuesDeliveryTimeoutKeepsConfirmedVersion(t *testing.T) {
	shrinkPollInterval(t)
	setSecretTestValues(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := secretValuesConfig(mock.url(), 2, `timeouts = { update = "1s" }`)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: secretValuesConfig(mock.url(), 1, `timeouts = { update = "1s" }`)},
		{PreConfig: func() {
			mock.setState(func(m *mockKupeAPI) {
				m.secretStatuses = []map[string]any{{"refresh": map[string]any{"requestedAt": "newer-token", "observedAt": "old-token"}}}
			})
		},
			Config: config, ExpectError: regexp.MustCompile(wrappedText("values saved as version 2; delivery is still in progress"))},
		{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.secretStatuses = nil }) }, Config: config,
			ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check:            acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 2, `"1"`), acc.TestCheckResourceAttr(secretAddr, "values_wo_version", "2"), acc.TestCheckResourceAttr(secretAddr, "source_version", "2"))},
	}})
}

func TestAccSecretValuesCreateTimeoutKeepsConfirmedVersion(t *testing.T) {
	shrinkPollInterval(t)
	setSecretTestValues(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.setState(func(m *mockKupeAPI) {
		m.secretStatuses = []map[string]any{{"observedGeneration": 0}}
	})
	config := secretValuesConfig(mock.url(), 1, `timeouts = { create = "1s" }`)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config, Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 1, ""), acc.TestCheckResourceAttr(secretAddr, "values_wo_version", "1"), acc.TestCheckResourceAttr(secretAddr, "source_version", "1"))},
		{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.secretStatuses = nil }) }, Config: config,
			ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: checkVaultWrites(mock, 1, "")},
	}})
}

func TestAccSecretValuesMissingStampRequestsRefresh(t *testing.T) {
	setSecretTestValues(t)
	mock := newMockKupeAPI()
	defer mock.close()
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: secretValuesConfig(mock.url(), 1, "")},
		{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.vaultSkipStamp = true }) }, Config: secretValuesConfig(mock.url(), 2, ""),
			Check: func(*terraform.State) error {
				mock.mu.Lock()
				defer mock.mu.Unlock()
				if mock.secretRefreshes != 1 {
					return fmt.Errorf("refresh calls %d, want 1", mock.secretRefreshes)
				}
				return nil
			}},
	}})
}

func TestAccSecretValuesWrittenBeforeTargetPatch(t *testing.T) {
	setSecretTestValues(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := strings.Replace(secretValuesConfig(mock.url(), 2, ""), `namespace = "app"`, `namespace = "next"`, 1)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: secretValuesConfig(mock.url(), 1, "")},
		{PreConfig: func() {
			mock.setState(func(m *mockKupeAPI) {
				m.secretBeforePatch = []func(map[string]any){func(map[string]any) {
					if m.vaultEntries["production/db-password"].version != 2 {
						t.Error("targets patched before values")
					}
				}}
			})
		},
			Config: config, Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 2, `"1"`), acc.TestCheckResourceAttr(secretAddr, "sync.0.namespace", "next"), acc.TestCheckResourceAttr(secretAddr, "source_version", "2"))},
	}})
}

const (
	secretCheckpointTokenGet    = "delivery token GET"
	secretCheckpointRefreshPost = "fallback refresh POST"
	secretCheckpointTargetPatch = "concurrent target PATCH"
)

// The mock handler already holds its mutex. These faults begin only after
// a confirmed PUT, leaving the real CLI's preceding planning reads usable.
func setSecretCheckpointFailure(m *mockKupeAPI, failure string) {
	switch failure {
	case secretCheckpointTokenGet:
		m.vaultAfterPut = func() { m.secretGetErrorStatus = http.StatusForbidden }
	case secretCheckpointRefreshPost:
		m.vaultSkipStamp = true
		m.secretRefreshErrorStatus = http.StatusForbidden
	case secretCheckpointTargetPatch:
		m.secretBeforePatch = []func(map[string]any){func(s map[string]any) {
			target := s["sync"].([]any)[0].(map[string]any)
			target["namespace"] = "external"
			s["generation"] = s["generation"].(int) + 1
		}}
	}
}

func clearSecretCheckpointFailure(m *mockKupeAPI) {
	m.secretGetErrorStatus = 0
	m.secretRefreshErrorStatus = 0
	m.vaultSkipStamp = false
	m.vaultAfterPut = nil
	m.secretBeforePatch = nil
}

func checkSecretConfirmedCheckpoint(mock *mockKupeAPI) secretPlanAction {
	return func(req plancheck.CheckPlanRequest) error {
		// Refresh can recover the metadata version, but cannot recover the
		// configured counter: it must have survived the failed apply itself.
		if err := priorSecretCounter(req, "2"); err != nil {
			return err
		}
		return checkVaultWrites(mock, 2, `"1"`)(nil)
	}
}

func TestAccSecretValuesConfirmedCheckpointSurvivesDeliveryRequestFailure(t *testing.T) {
	setSecretTestValues(t)
	for _, failure := range []string{secretCheckpointTokenGet, secretCheckpointRefreshPost} {
		t.Run(failure, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			config := secretValuesConfig(mock.url(), 2, "")
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: secretValuesConfig(mock.url(), 1, "")},
				{PreConfig: func() { mock.setState(func(m *mockKupeAPI) { setSecretCheckpointFailure(m, failure) }) }, Config: config,
					ExpectError: regexp.MustCompile(wrappedText("values saved; delivery not confirmed"))},
				{PreConfig: func() { mock.setState(clearSecretCheckpointFailure) }, Config: config,
					ConfigPlanChecks:  acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{checkSecretConfirmedCheckpoint(mock), plancheck.ExpectEmptyPlan(), secretNoValuesPlan{}}},
					ConfigStateChecks: []statecheck.StateCheck{secretNoValuesState{}},
					Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 2, `"1"`), acc.TestCheckResourceAttr(secretAddr, "values_wo_version", "2"),
						acc.TestCheckResourceAttr(secretAddr, "source_version", "2"), acc.TestCheckNoResourceAttr(secretAddr, "values_wo.%"))},
			}})
			if failure == secretCheckpointRefreshPost {
				mock.mu.Lock()
				defer mock.mu.Unlock()
				if mock.secretRefreshes != 1 {
					t.Error("failed refresh POST was repeated during recovery")
				}
			}
		})
	}
}

func TestAccSecretValuesConfirmedCheckpointSurvivesTargetRefusal(t *testing.T) {
	setSecretTestValues(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := strings.Replace(secretValuesConfig(mock.url(), 2, ""), `namespace = "app"`, `namespace = "next"`, 1)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: secretValuesConfig(mock.url(), 1, "")},
		{PreConfig: func() {
			mock.setState(func(m *mockKupeAPI) { setSecretCheckpointFailure(m, secretCheckpointTargetPatch) })
		}, Config: config,
			ExpectError: regexp.MustCompile(wrappedText("secret changed outside this apply"))},
		{PreConfig: func() { mock.setState(clearSecretCheckpointFailure) }, Config: config,
			ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{checkSecretConfirmedCheckpoint(mock), secretNoValuesPlan{},
				plancheck.ExpectResourceAction(secretAddr, plancheck.ResourceActionUpdate), secretPlanAction(func(req plancheck.CheckPlanRequest) error {
					if req.Plan.PriorState == nil || req.Plan.PriorState.Values == nil || req.Plan.PriorState.Values.RootModule == nil {
						return fmt.Errorf("refreshed secret prior state is absent")
					}
					for _, r := range req.Plan.PriorState.Values.RootModule.Resources {
						if r.Address != secretAddr {
							continue
						}
						targets, ok := r.AttributeValues["sync"].([]any)
						if !ok || len(targets) != 1 {
							return fmt.Errorf("remaining target change is absent from refreshed prior state")
						}
						target, ok := targets[0].(map[string]any)
						if !ok || target["namespace"] != "external" {
							return fmt.Errorf("unaccepted target settings were recorded as applied")
						}
						return nil
					}
					return fmt.Errorf("refreshed secret prior state is absent")
				})}},
			ConfigStateChecks: []statecheck.StateCheck{secretNoValuesState{}},
			Check: acc.ComposeAggregateTestCheckFunc(checkVaultWrites(mock, 2, `"1"`), acc.TestCheckResourceAttr(secretAddr, "values_wo_version", "2"),
				acc.TestCheckResourceAttr(secretAddr, "source_version", "2"), acc.TestCheckResourceAttr(secretAddr, "sync.0.namespace", "next"))},
	}})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.secretRequests) != 3 || mock.secretRequests[1].method != http.MethodPatch || mock.secretRequests[2].method != http.MethodPatch {
		t.Error("recovery did not retry only the remaining target PATCH")
	}
}

func secretCheckpointSync(namespace string) types.List {
	return types.ListValueMust(types.ObjectType{AttrTypes: syncTargetAttrTypes}, []attr.Value{
		types.ObjectValueMust(syncTargetAttrTypes, map[string]attr.Value{
			"cluster": types.StringValue("prod"), "namespace": types.StringValue(namespace),
			"secret_name": types.StringNull(), "keys": types.SetNull(types.StringType),
		}),
	})
}

// The CLI tests above pin persisted counters and no-resend behaviour. This
// direct lifecycle check inspects BOTH confirmed versions and the accepted
// targets immediately on error, before any refresh can recover metadata.
func TestSecretValuesConfirmedCheckpointBeforeFollowup(t *testing.T) {
	for _, failure := range []string{secretCheckpointTokenGet, secretCheckpointRefreshPost, secretCheckpointTargetPatch} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			mock := newMockKupeAPI()
			defer mock.close()
			r := NewSecretResource().(*SecretResource)
			r.client = client.New(mock.url(), "acme", "test-token")
			s := testResourceSchema(t, r)
			raw := testPlanValue(t, s, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "db-password"), "secret_path": tftypes.NewValue(tftypes.String, "production/db-password"), "values_wo_version": tftypes.NewValue(tftypes.Number, 1)})
			createPlan := tfsdk.Plan{Schema: s.Schema, Raw: raw}
			if d := createPlan.SetAttribute(ctx, path.Root("sync"), secretCheckpointSync("app")); d.HasError() {
				t.Fatal("could not prepare create targets")
			}
			created := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: createPlan.Raw.Copy()}}
			r.Create(ctx, resource.CreateRequest{Plan: createPlan, Config: secretConfiguredValues(t, createPlan)}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal("initial confirmed create failed")
			}
			if err := checkVaultWrites(mock, 1, "")(nil); err != nil {
				t.Fatal("initial create did not confirm exactly one PUT")
			}
			var prior SecretResourceModel
			if d := created.State.Get(ctx, &prior); d.HasError() {
				t.Fatal("could not decode initial state")
			}
			plan := tfsdk.Plan{Schema: s.Schema, Raw: created.State.Raw.Copy()}
			if d := plan.SetAttribute(ctx, path.Root("values_wo_version"), types.Int64Value(2)); d.HasError() {
				t.Fatal("could not prepare replacement counter")
			}
			if failure == secretCheckpointTargetPatch {
				if d := plan.SetAttribute(ctx, path.Root("sync"), secretCheckpointSync("next")); d.HasError() {
					t.Fatal("could not prepare changed targets")
				}
			}
			mock.setState(func(m *mockKupeAPI) { setSecretCheckpointFailure(m, failure) })
			response := resource.UpdateResponse{State: created.State}
			r.Update(ctx, resource.UpdateRequest{Plan: plan, State: created.State, Config: secretConfiguredValues(t, plan)}, &response)
			if !response.Diagnostics.HasError() {
				t.Fatal("post-PUT follow-up failure was not reported")
			}
			if strings.Contains(secretDiagnosticsText(response.Diagnostics), secretValueSentinel) {
				t.Error("follow-up diagnostic exposed a value")
			}
			wantDiagnostic := "values saved; delivery not confirmed"
			if failure == secretCheckpointTargetPatch {
				wantDiagnostic = "secret changed outside this apply"
			}
			if !strings.Contains(secretDiagnosticsText(response.Diagnostics), wantDiagnostic) {
				t.Fatal("the intended post-PUT failure was not reached")
			}
			var confirmed SecretResourceModel
			if d := response.State.Get(ctx, &confirmed); d.HasError() {
				t.Fatal("could not decode returned confirmed state")
			}
			if confirmed.ValuesWOVersion.ValueInt64() != 2 || confirmed.SourceVersion.ValueInt64() != 2 {
				t.Fatal("early confirmed checkpoint lost counter or source version before follow-up failure")
			}
			if !confirmed.ValuesWO.IsNull() {
				t.Error("returned confirmed state retained a write-only value")
			}
			if !confirmed.Sync.Equal(prior.Sync) {
				t.Error("unaccepted target settings were recorded before a successful PATCH")
			}
			if err := checkVaultWrites(mock, 2, `"1"`)(nil); err != nil {
				t.Fatal(err)
			}
			mock.setState(clearSecretCheckpointFailure)
			refreshed := resource.ReadResponse{State: response.State}
			r.Read(ctx, resource.ReadRequest{State: response.State}, &refreshed)
			if refreshed.Diagnostics.HasError() {
				t.Fatal("recovery refresh failed")
			}
			recoveryPlan := tfsdk.Plan{Schema: s.Schema, Raw: refreshed.State.Raw.Copy()}
			if failure == secretCheckpointTargetPatch {
				if d := recoveryPlan.SetAttribute(ctx, path.Root("sync"), secretCheckpointSync("next")); d.HasError() {
					t.Fatal("could not prepare remaining target change")
				}
			}
			recovered := resource.UpdateResponse{State: refreshed.State}
			r.Update(ctx, resource.UpdateRequest{Plan: recoveryPlan, State: refreshed.State, Config: secretConfiguredValues(t, recoveryPlan)}, &recovered)
			if recovered.Diagnostics.HasError() {
				t.Fatal("unchanged-counter recovery failed")
			}
			if err := checkVaultWrites(mock, 2, `"1"`)(nil); err != nil {
				t.Fatal("unchanged-counter recovery resent values")
			}
			if d := recovered.State.Get(ctx, &confirmed); d.HasError() {
				t.Fatal("could not decode recovered state")
			}
			if confirmed.ValuesWOVersion.ValueInt64() != 2 || confirmed.SourceVersion.ValueInt64() != 2 || !confirmed.ValuesWO.IsNull() {
				t.Error("recovery changed the confirmed value checkpoint")
			}
			if failure == secretCheckpointTargetPatch && !confirmed.Sync.Equal(secretCheckpointSync("next")) {
				t.Error("recovery did not complete the remaining target change")
			}
		})
	}
}

func secretConfiguredValues(t *testing.T, plan tfsdk.Plan) tfsdk.Config {
	t.Helper()
	config := plan
	if d := config.SetAttribute(context.Background(), path.Root("values_wo"), types.MapValueMust(types.StringType, map[string]attr.Value{"password": types.StringValue(secretValueSentinel)})); d.HasError() {
		t.Fatal(d)
	}
	return tfsdk.Config(config)
}

func TestSecretValuesUnsupportedClient(t *testing.T) {
	ctx := context.Background()
	r := NewSecretResource().(*SecretResource)
	s := testResourceSchema(t, r)
	raw := testPlanValue(t, s, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "app"), "secret_path": tftypes.NewValue(tftypes.String, "apps/config"), "values_wo_version": tftypes.NewValue(tftypes.Number, 1)})
	config := secretConfiguredValues(t, tfsdk.Plan{Schema: s.Schema, Raw: raw})
	dynamic, err := tfprotov6.NewDynamicValue(config.Raw.Type(), config.Raw)
	if err != nil {
		t.Fatal(err)
	}
	server, err := testAccProtoV6ProviderFactories()["kupe"]()
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.ValidateResourceConfig(ctx, &tfprotov6.ValidateResourceConfigRequest{TypeName: "kupe_secret", Config: &dynamic, ClientCapabilities: &tfprotov6.ValidateResourceConfigClientCapabilities{WriteOnlyAttributesAllowed: false}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range response.Diagnostics {
		if strings.Contains(d.Detail, secretValueSentinel) {
			t.Error("diagnostic leaked a value")
		}
		found = found || strings.Contains(d.Detail, "Write-only attributes are only supported in Terraform 1.11 and later")
	}
	if !found {
		t.Fatalf("missing unsupported-client diagnostic: %v", response.Diagnostics)
	}
}

func TestSecretValuesDiagnosticsAndPriorState(t *testing.T) {
	for _, tt := range []struct {
		name  string
		fault mockVaultFault
		want  string
	}{
		{"unconfirmed", mockVaultFault{status: 503, code: client.CodeSecretWriteUnconfirmed}, "may or may not"},
		{"body read then disconnect", mockVaultFault{status: 503, disconnect: true}, "may or may not"},
		{"upstream echo", mockVaultFault{status: 400, code: "invalid_secret_values"}, "could not be completed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			r := NewSecretResource().(*SecretResource)
			r.client = client.New(mock.url(), "acme", "test-token")
			s := testResourceSchema(t, r)
			raw := testPlanValue(t, s, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "db-password"), "secret_path": tftypes.NewValue(tftypes.String, "production/db-password"), "values_wo_version": tftypes.NewValue(tftypes.Number, 1)})
			createPlan := tfsdk.Plan{Schema: s.Schema, Raw: raw}
			created := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: raw.Copy()}}
			r.Create(context.Background(), resource.CreateRequest{Plan: createPlan, Config: secretConfiguredValues(t, createPlan)}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}
			plan := tfsdk.Plan{Schema: s.Schema, Raw: created.State.Raw.Copy()}
			if d := plan.SetAttribute(context.Background(), path.Root("values_wo_version"), types.Int64Value(2)); d.HasError() {
				t.Fatal(d)
			}
			mock.setState(func(m *mockKupeAPI) { m.vaultFaults = []mockVaultFault{tt.fault} })
			response := resource.UpdateResponse{State: created.State}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: created.State, Config: secretConfiguredValues(t, plan)}, &response)
			text := secretDiagnosticsText(response.Diagnostics)
			if !response.Diagnostics.HasError() || !strings.Contains(text, tt.want) || strings.Contains(text, secretValueSentinel) {
				t.Error("diagnostic did not safely describe the failed write")
			}
			var state SecretResourceModel
			if d := response.State.Get(context.Background(), &state); d.HasError() {
				t.Fatal(d)
			}
			mock.mu.Lock()
			requests := len(mock.vaultRequests)
			mock.mu.Unlock()
			if state.ValuesWOVersion.ValueInt64() != 1 || state.SourceVersion.ValueInt64() != 1 || !state.ValuesWO.IsNull() || requests != 2 {
				t.Error("failed write changed confirmed state or retried PUT")
			}
		})
	}
}

func TestSecretConfirmedCreateTimeoutWarningAndTerminalError(t *testing.T) {
	shrinkPollInterval(t)
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal %v", terminal), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			status := map[string]any{"observedGeneration": 0}
			if terminal {
				status = map[string]any{"phase": "Deleting"}
			}
			mock.setState(func(m *mockKupeAPI) { m.secretStatuses = []map[string]any{status} })
			r := NewSecretResource().(*SecretResource)
			r.client = client.New(mock.url(), "acme", "test-token")
			s := testResourceSchema(t, r)
			timeoutType := s.Schema.Type().TerraformType(context.Background()).(tftypes.Object).AttributeTypes["timeouts"].(tftypes.Object)
			timeoutValues := map[string]tftypes.Value{}
			for key, typ := range timeoutType.AttributeTypes {
				timeoutValues[key] = tftypes.NewValue(typ, nil)
			}
			timeoutValues["create"] = tftypes.NewValue(tftypes.String, "80ms")
			raw := testPlanValue(t, s, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "db-password"), "secret_path": tftypes.NewValue(tftypes.String, "production/db-password"), "values_wo_version": tftypes.NewValue(tftypes.Number, 1), "timeouts": tftypes.NewValue(timeoutType, timeoutValues)})
			plan := tfsdk.Plan{Schema: s.Schema, Raw: raw}
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: raw.Copy()}}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: secretConfiguredValues(t, plan)}, &resp)
			if resp.Diagnostics.HasError() != terminal {
				t.Fatalf("wrong confirmed-create severity: %v", resp.Diagnostics)
			}
			if !terminal && (len(warnings(resp.Diagnostics)) != 1 || !strings.Contains(secretDiagnosticsText(resp.Diagnostics), "values saved as version 1; delivery is still in progress") || strings.Contains(secretDiagnosticsText(resp.Diagnostics), "taint")) {
				t.Error("confirmed-create timeout lost warning or gave replacement advice")
			}
			var state SecretResourceModel
			if d := resp.State.Get(context.Background(), &state); d.HasError() {
				t.Fatal(d)
			}
			mock.mu.Lock()
			requests := len(mock.vaultRequests)
			mock.mu.Unlock()
			if state.ValuesWOVersion.ValueInt64() != 1 || state.SourceVersion.ValueInt64() != 1 || !state.ValuesWO.IsNull() || requests != 1 {
				t.Error("confirmed version not saved before waiting")
			}
		})
	}
}

func secretDiagnosticsText(diags diag.Diagnostics) string {
	var text string
	for _, d := range diags {
		text += d.Summary() + " " + d.Detail() + "\n"
	}
	return text
}

func TestSecretReadSourceUnavailableKeepsVersion(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	r := NewSecretResource().(*SecretResource)
	r.client = client.New(mock.url(), "acme", "test-token")
	for _, tt := range []struct {
		status              int
		code                string
		wantError, wantNull bool
	}{{404, "", false, true}, {404, client.CodeVaultNotConfigured, true, false}, {503, client.CodeVaultUnavailable, false, false}} {
		mock.setState(func(m *mockKupeAPI) { m.vaultReadStatus, m.vaultReadCode = tt.status, tt.code })
		state := SecretResourceModel{SecretPath: types.StringValue("apps/config"), SourceVersion: types.Int64Value(8)}
		var diags diag.Diagnostics
		r.readSourceVersion(context.Background(), &state, &diags)
		if diags.HasError() != tt.wantError || state.SourceVersion.IsNull() != tt.wantNull || (!tt.wantNull && state.SourceVersion.ValueInt64() != 8) {
			t.Errorf("metadata status %d code %s changed state incorrectly", tt.status, tt.code)
		}
		if tt.status == http.StatusServiceUnavailable && len(warnings(diags)) != 1 {
			t.Error("metadata outage missing warning")
		}
	}
}

func TestSecretSourceVersionPlanModifier(t *testing.T) {
	ctx := context.Background()
	r := NewSecretResource().(*SecretResource)
	s := testResourceSchema(t, r)
	for _, tt := range []struct {
		name           string
		prior, planned any
		unknown        bool
	}{
		{"same version", 1, 1, false}, {"unchanged omitted version", nil, nil, false},
		{"bumped version", 1, 2, true}, {"adoption", nil, 1, true}, {"removal", 1, nil, true},
		{"unknown version", 1, tftypes.UnknownValue, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prior := testPlanValue(t, s, map[string]tftypes.Value{"values_wo_version": tftypes.NewValue(tftypes.Number, tt.prior)})
			planned := testPlanValue(t, s, map[string]tftypes.Value{"values_wo_version": tftypes.NewValue(tftypes.Number, tt.planned)})
			resp := planmodifier.Int64Response{PlanValue: types.Int64Unknown()}
			secretSourceVersionModifier{}.PlanModifyInt64(ctx, planmodifier.Int64Request{Plan: tfsdk.Plan{Schema: s.Schema, Raw: planned}, State: tfsdk.State{Schema: s.Schema, Raw: prior}, StateValue: types.Int64Value(8)}, &resp)
			if resp.Diagnostics.HasError() || resp.PlanValue.IsUnknown() != tt.unknown || (!tt.unknown && resp.PlanValue.ValueInt64() != 8) {
				t.Errorf("source version plan = %v, %v", resp.PlanValue, resp.Diagnostics)
			}
		})
	}
}

func TestSecretRefreshWaitFollowsNewerToken(t *testing.T) {
	r := NewSecretResource().(*SecretResource)
	lifecycle := r.valuesLifecycle(types.Int64Value(4), "old-token")
	s := &client.Secret{Name: "app", Generation: 1, Sync: []client.SyncTarget{{Cluster: "prod", Namespace: "app"}}, Status: &client.SecretStatus{
		Phase: "Active", ObservedGeneration: 1, Refresh: &client.SecretRefreshStatus{RequestedAt: "new-token", ObservedAt: "old-token"},
		SyncStatuses: []client.SecretTargetStatus{{Cluster: "prod", Namespace: "app", SecretName: "app", Phase: "Synced", DeliveredRefresh: "old-token"}},
	}}
	if lifecycle.kind.ready.converged(s) {
		t.Error("accepted old observed token")
	}
	s.Status.Refresh.ObservedAt = "new-token"
	if lifecycle.kind.ready.converged(s) {
		t.Error("accepted old target token")
	}
	s.Status.SyncStatuses[0].DeliveredRefresh = "new-token"
	if !lifecycle.kind.ready.converged(s) {
		t.Error("did not follow and confirm newer token")
	}
	if !secretKind.ready.converged(s) {
		t.Error("operation modified the shared readiness predicate")
	}
}

func TestAccSecretValuesValidation(t *testing.T) {
	for _, tt := range []struct{ name, extra, want string }{
		{"empty values", `values_wo = {} values_wo_version = 1`, "at least 1"},
		{"invalid key", `values_wo = { "invalid/key" = "test-value" } values_wo_version = 1`, "Invalid secret key"},
		{"null map value", `values_wo = { password = null } values_wo_version = 1`, "Null Map Value"},
		{"zero counter", `values_wo = { password = "test-value" } values_wo_version = 0`, "at least 1"},
		{"missing counter", `values_wo = { password = "test-value" }`, "Invalid Attribute Combination"},
		{"missing values", `values_wo_version = 1`, "Invalid Attribute Combination"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			// HCL needs a newline between top-level arguments.
			extra := strings.ReplaceAll(tt.extra, " values_wo_version", "\nvalues_wo_version")
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{{Config: secretConfig(mock.url(), "", extra), ExpectError: regexp.MustCompile(wrappedText(tt.want))}}})
			mock.mu.Lock()
			reachedAPI := len(mock.vaultRequests) != 0 || len(mock.secretRequests) != 0
			mock.mu.Unlock()
			if reachedAPI {
				t.Error("invalid values configuration reached API")
			}
		})
	}
}
