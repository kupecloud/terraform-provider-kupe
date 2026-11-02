package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func testAccAppConfig(host string, protected bool) string {
	return testAccProviderConfig(host) + fmt.Sprintf(`
resource "kupe_app" "test" {
  name = "shop"
  deletion_protection = %t
}
`, protected)
}

func TestAccAppResourceLifecycleAndImport(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: testAccAppConfig(mock.url(), true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("kupe_app.test", "name", "shop"), resource.TestCheckResourceAttr("kupe_app.test", "phase", "Running"))},
		{ResourceName: "kupe_app.test", ImportState: true, ImportStateId: "shop", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name"},
		{Config: testAccAppConfig(mock.url(), false), Check: resource.TestCheckResourceAttr("kupe_app.test", "deletion_protection", "false")},
	}})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.apps) != 0 {
		t.Fatal("destroy left an app behind")
	}
	for _, req := range mock.appRequests {
		if req.method == "DELETE" {
			if req.ifMatch == "" || req.body["confirm"] != "shop" {
				t.Fatalf("unsafe delete request: %+v", req)
			}
			if children, ok := req.body["environments"].([]any); !ok || len(children) != 0 {
				t.Fatalf("delete implicitly authorised unmanaged children: %+v", req.body)
			}
		}
	}
}

func TestAccAppResourceProtectedDestroyPreservesIdentity(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: testAccAppConfig(mock.url(), true)},
		{Config: testAccAppConfig(mock.url(), true), Destroy: true, ExpectError: regexp.MustCompile("(?s)app has deletion protection enabled.*deletion_protection = false")},
		{Config: testAccAppConfig(mock.url(), false)},
	}})
}

func TestAccAppResourceDisabledCreate(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	mock.appsDisabled = true
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{
		Config: testAccAppConfig(mock.url(), false), ExpectError: regexp.MustCompile("(?s)apps_unavailable.*Apps are not enabled"),
	}}})
}

func TestAccAppResourceUnavailableRefreshKeepsState(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
		{Config: testAccAppConfig(mock.url(), false)},
		{Config: testAccAppConfig(mock.url(), false), PlanOnly: true, PreConfig: func() { mock.mu.Lock(); mock.appReadsUnavailable = true; mock.mu.Unlock() }},
		{Config: testAccAppConfig(mock.url(), false), PreConfig: func() { mock.mu.Lock(); mock.appReadsUnavailable = false; mock.mu.Unlock() }},
	}})
}

// F1: the first stale PATCH must not be retried against a same-name App UID.
func TestAccAppResourceStaleUpdatePinsOriginalUID(t *testing.T) {
	shrinkPollInterval(t)
	for _, replacement := range []bool{true, false} {
		t.Run(fmt.Sprintf("replacement=%v", replacement), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			var originalUID string
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{
				{Config: testAccAppConfig(mock.url(), true), Check: func(*terraform.State) error {
					mock.mu.Lock()
					defer mock.mu.Unlock()
					originalUID = mock.apps["shop"]["uid"].(string)
					return nil
				}},
				{Config: testAccAppConfig(mock.url(), false), PreConfig: func() {
					mock.mu.Lock()
					mock.appBeforePatch = []func(map[string]any){func(app map[string]any) {
						if replacement {
							app["uid"] = "same-name-replacement"
							app["generation"] = 1
						} else {
							app["generation"] = app["generation"].(int) + 1
						}
					}, func(app map[string]any) {
						if replacement {
							t.Error("stale retry targeted the replacement App")
						}
					}}
					mock.mu.Unlock()
				}, ExpectError: func() *regexp.Regexp {
					if replacement {
						return regexp.MustCompile("(?s)" + wrappedText(appIdentityChangedMessage))
					}
					return nil
				}()},
				{Config: testAccAppConfig(mock.url(), replacement), PlanOnly: true, PreConfig: func() {
					mock.mu.Lock()
					defer mock.mu.Unlock()
					patches := 0
					for _, request := range mock.appRequests {
						if request.method == "PATCH" {
							patches++
							if !strings.Contains(request.ifMatch, originalUID) {
								t.Fatalf("PATCH targeted another App identity: %q", request.ifMatch)
							}
						}
					}
					want := 2
					if replacement {
						want = 1
						if mock.apps["shop"]["deletionProtection"] != true {
							t.Fatal("replacement App protection was modified")
						}
					}
					if patches != want {
						t.Fatalf("PATCH requests=%d, want %d", patches, want)
					}
					// Disarm only after the failed apply's safety assertions; a later
					// explicit plan/apply may legitimately modify the replacement.
					mock.appBeforePatch = nil
				}},
				// A later explicit plan may manage the replacement. It is separate
				// from the failed apply's forbidden automatic stale retry.
				{Config: testAccAppConfig(mock.url(), false)},
			}})
		})
	}
}

func TestAppWaitRejectsReplacementBeforeRecordingIt(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.apps["shop"] = map[string]any{"name": "shop", "uid": "replacement", "generation": 1, "deletionProtection": true, "createdAt": "new-time"}
	r := NewAppResource().(*AppResource)
	r.client = client.New(mock.url(), "acme", "test-token")
	sch := testResourceSchema(t, r)
	accepted := &client.App{Name: "shop", UID: "original", Generation: 1, DeletionProtection: true, CreatedAt: "original-time"}
	m := AppResourceModel{Timeouts: typesTimeoutsNull(t, sch)}
	mapAppToState(accepted, `"original.1"`, &m)
	state := tfsdk.State{Schema: sch.Schema}
	if d := state.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	var diags diag.Diagnostics
	r.waitAppObserved(context.Background(), time.Second, "update", accepted, &m, &state, &diags)
	if !diags.HasError() {
		t.Fatal("replacement App verified the original accepted request")
	}
	if !m.CreatedAt.Equal(types.StringValue("original-time")) {
		t.Fatal("replacement App was adopted while waiting")
	}
}
