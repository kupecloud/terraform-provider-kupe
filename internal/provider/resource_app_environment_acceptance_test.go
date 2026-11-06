package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	acc "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const appEnvironmentAddr = "kupe_app_environment.test"

func testAccAppEnvironmentConfig(host, digest, extra string) string {
	return testAccProviderConfig(host) + fmt.Sprintf(`
resource "kupe_app" "environment_parent" {
  name = "shop"
  deletion_protection = false
}
resource "kupe_app_environment" "test" {
  app = kupe_app.environment_parent.name
  name = "production"
  cluster = "europe"
  deletion_protection = false
  web = {
    image = "ghcr.io/kupecloud/hello-kupe@sha256:%s"
    port = 8080
  }
%s
}
`, strings.Repeat(digest, 64), extra)
}

func appEnvironmentRequestCount(mock *mockKupeAPI, method, suffix string) int {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	count := 0
	for _, request := range mock.appEnvironmentRequests {
		if request.method == method && strings.HasSuffix(request.path, suffix) {
			count++
		}
	}
	return count
}

func checkAppEnvironmentNotTainted(state *terraform.State) error {
	instance := state.RootModule().Resources[appEnvironmentAddr]
	if instance == nil || instance.Primary == nil {
		return fmt.Errorf("environment missing from state")
	}
	if instance.Primary.Tainted {
		return fmt.Errorf("failed first deployment was tainted")
	}
	return nil
}

func TestAccAppEnvironmentLifecycleImportAndRetry(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := testAccAppEnvironmentConfig(mock.url(), "a", "")
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config, Check: acc.ComposeTestCheckFunc(acc.TestCheckResourceAttr(appEnvironmentAddr, "phase", "Running"), acc.TestCheckResourceAttr(appEnvironmentAddr, "deployment_revision", "1"))},
		{ResourceName: appEnvironmentAddr, ImportState: true, ImportStateId: "shop/production", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name"},
		{Config: testAccAppEnvironmentConfig(mock.url(), "a", "deployment_revision = 2"), Check: acc.TestCheckResourceAttr(appEnvironmentAddr, "deployment_revision", "2")},
		{Config: testAccAppEnvironmentConfig(mock.url(), "b", "deployment_revision = 3\ndomains = [\"shop.example.com\"]"), Check: acc.TestCheckResourceAttr(appEnvironmentAddr, "deployment_revision", "3")},
	}})
	if got := appEnvironmentRequestCount(mock, "POST", "/retry"); got != 2 {
		t.Fatalf("retry POSTs = %d, want 2", got)
	}
	if got := appEnvironmentRequestCount(mock, "PATCH", "/production"); got != 1 {
		t.Fatalf("PATCH requests = %d, want one combined-settings patch", got)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.appEnvironments) != 0 || len(mock.apps) != 0 {
		t.Fatal("destroy did not confirm both resources gone")
	}
	var lastPatchETag string
	for _, request := range mock.appEnvironmentRequests {
		if request.method == "PATCH" {
			lastPatchETag = request.etag
		}
		if request.method == "POST" && strings.HasSuffix(request.path, "/retry") && lastPatchETag != "" && request.etag == lastPatchETag {
			t.Fatal("combined retry reused the pre-PATCH ETag")
		}
	}
}

func TestAccAppEnvironmentFirstFailureAndTimeoutRemainUntainted(t *testing.T) {
	shrinkPollInterval(t)
	for _, outcome := range []string{"Failed", "Deploying"} {
		t.Run(outcome, func(t *testing.T) {
			capture := captureAppCLIOutput(t)
			mock := newMockKupeAPI()
			defer mock.close()
			mock.appEnvironmentOutcome = outcome
			extra := ""
			if outcome == "Deploying" {
				extra = "timeouts = { create = \"1s\" }"
			}
			config := testAccAppEnvironmentConfig(mock.url(), "a", extra)
			checkWarning := func(*terraform.State) error {
				output, err := capture.read()
				if err != nil {
					return err
				}
				output = strings.Join(strings.Fields(strings.ReplaceAll(output, "│", "")), " ")
				for _, want := range []string{
					"Warning: app deployment did not complete",
					"The request was accepted and is recorded in Terraform state.",
					"increase `deployment_revision` by one and apply.",
				} {
					if !strings.Contains(output, want) {
						return fmt.Errorf("CLI did not render App deployment warning text %q", want)
					}
				}
				if outcome == "Failed" && !regexp.MustCompile(`Requested release .* ended Failed, reason "TestOutcome"`).MatchString(output) {
					return fmt.Errorf("CLI did not render the failed requested release")
				}
				if outcome == "Deploying" && !strings.Contains(output, "The create timeout ended before the requested release completed.") {
					return fmt.Errorf("CLI did not render the create timeout warning")
				}
				return nil
			}
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: config, Check: acc.ComposeTestCheckFunc(checkAppEnvironmentNotTainted, checkWarning)},
				{Config: config, PlanOnly: true},
			}})
			if got := appEnvironmentRequestCount(mock, "POST", "/environments"); got != 1 {
				t.Fatalf("failed first deployment was recreated %d times", got)
			}
		})
	}
}

func TestAccAppEnvironmentFailedUpdateRecordsSettingsWithoutResend(t *testing.T) {
	shrinkPollInterval(t)
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%v", recovery), func(t *testing.T) {
			// The same observer must preserve the non-zero CLI exit below.
			captureAppCLIOutput(t)
			mock := newMockKupeAPI()
			defer mock.close()
			var before int
			failed := testAccAppEnvironmentConfig(mock.url(), "b", "")
			want := "(?s)Requested release.*Failed.*TestOutcome"
			if recovery {
				want += ".*Recovery phase.*Succeeded.*serving-previous.*now serving"
			}
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: testAccAppEnvironmentConfig(mock.url(), "a", "")},
				{Config: failed, PreConfig: func() {
					mock.mu.Lock()
					mock.appEnvironmentOutcome = "Failed"
					mock.appEnvironmentRecovery = recovery
					mock.mu.Unlock()
				}, ExpectError: regexp.MustCompile(want)},
				{Config: failed, PlanOnly: true, PreConfig: func() { before = appEnvironmentRequestCount(mock, "PATCH", "/production") }},
				{Config: failed, Check: func(*terraform.State) error {
					if got := appEnvironmentRequestCount(mock, "PATCH", "/production"); got != before {
						return fmt.Errorf("unchanged apply resent failed update: %d -> %d", before, got)
					}
					return nil
				}},
			}})
		})
	}
}

func TestAccAppEnvironmentRevisionInvalidAtPlan(t *testing.T) {
	shrinkPollInterval(t)
	for _, revision := range []int{0, 3} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: testAccAppEnvironmentConfig(mock.url(), "a", "")},
				{Config: testAccAppEnvironmentConfig(mock.url(), "a", fmt.Sprintf("deployment_revision = %d", revision)), PlanOnly: true, ExpectError: regexp.MustCompile("(?s)(deployment_revision can only be increased by one per apply|Invalid Attribute Value)")},
				{Config: testAccAppEnvironmentConfig(mock.url(), "a", "")},
			}})
		})
	}
}

func TestAccAppEnvironmentDomainOnlyDoesNotWaitForDeployment(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.appEnvironmentOutcome = "Deploying"
	config := testAccAppEnvironmentConfig(mock.url(), "a", "timeouts = { create = \"1s\", update = \"1s\" }")
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config},
		{Config: testAccAppEnvironmentConfig(mock.url(), "a", "domains = [\"shop.example.com\"]\ntimeouts = { create = \"1s\", update = \"1s\" }"), Check: acc.TestCheckResourceAttr(appEnvironmentAddr, "domains.#", "1")},
	}})
	if got := appEnvironmentRequestCount(mock, "POST", "/retry"); got != 0 {
		t.Fatalf("domain edit retried %d times", got)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.appReleases["shop/production"]) != 1 {
		t.Fatal("domain-only patch created a release")
	}
}

func TestAccAppEnvironmentUnreferencedSecretEditMakesNoRelease(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	secrets := func(path string) string {
		return fmt.Sprintf("secrets = [{ name = \"unused\", path = %q }]", path)
	}
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: testAccAppEnvironmentConfig(mock.url(), "a", secrets("apps/old"))},
		{Config: testAccAppEnvironmentConfig(mock.url(), "a", secrets("apps/new")), Check: acc.TestCheckResourceAttr(appEnvironmentAddr, "secrets.0.path", "apps/new")},
	}})
	if got := appEnvironmentRequestCount(mock, http.MethodPatch, ""); got == 0 {
		t.Fatal("the secret edit never reached the API, so the test proves nothing")
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if got := len(mock.appReleases["shop/production"]); got != 1 {
		t.Fatalf("editing a secret no container references made %d releases, want 1", got)
	}
}

func TestMockReleaseSecretsOnlyCountsReferencedSecrets(t *testing.T) {
	web := &client.AppWeb{Env: []client.AppEnvVar{{Name: "A", ValueFrom: &client.AppValueFrom{SecretKeyRef: client.AppSecretKeyRef{Secret: "used", Key: "k"}}}}}
	got := mockReleaseSecrets(web, []client.AppSecret{{Name: "used", Path: "apps/u"}, {Name: "unused", Path: "apps/x"}})
	if len(got) != 1 || got[0].Name != "used" {
		t.Fatalf("release secrets = %+v, want only the referenced one", got)
	}
	if got := mockReleaseSecrets(nil, []client.AppSecret{{Name: "used"}}); len(got) != 0 {
		t.Fatalf("nil web produced %+v", got)
	}
}

func TestAccAppEnvironmentConservativeAmbiguityDoesNotResubmit(t *testing.T) {
	shrinkPollInterval(t)
	for _, foreignConfig := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign=%v", foreignConfig), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			config := testAccAppEnvironmentConfig(mock.url(), "b", "")
			var afterFailure int
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: testAccAppEnvironmentConfig(mock.url(), "a", "")},
				{Config: config, PreConfig: func() {
					mock.mu.Lock()
					mock.appEnvironmentAfterWrite = []func(*client.AppEnvironment, *[]client.AppRelease){func(env *client.AppEnvironment, history *[]client.AppRelease) { // Complete a real B -> C -> B sequence before the first
						// provider observation; generation/revision/runtime alone cannot identify B.
						acceptedImage := env.Web.Image
						if foreignConfig {
							env.Generation++
							env.Web.Image = "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("c", 64)
							mock.appMockRelease("shop/production", env)
						} else {
							env.Generation++
							env.Web.Image = "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("c", 64)
							mock.appMockRelease("shop/production", env)
							env.Generation++
							env.Web.Image = acceptedImage
							mock.appMockRelease("shop/production", env)
						}
						env.Status.ObservedGeneration = env.Generation
						*history = mock.appReleases["shop/production"]
						mock.appEnvironmentOnGet = nil
					}}
					mock.mu.Unlock()
				}, ExpectError: regexp.MustCompile("(?s)" + wrappedText(appReleaseUnidentified))},
				{Config: config, PlanOnly: true, ExpectNonEmptyPlan: foreignConfig, PreConfig: func() { afterFailure = appEnvironmentRequestCount(mock, "PATCH", "/production") }},
				// A user-driven apply after refreshed foreign drift is a new
				// request, unlike any automatic resend within the failed apply.
				{Config: config, Check: func(*terraform.State) error {
					got := appEnvironmentRequestCount(mock, "PATCH", "/production")
					want := afterFailure
					if foreignConfig {
						want++
					}
					if got != want {
						return fmt.Errorf("PATCH requests = %d, want %d after explicit next apply", got, want)
					}
					return nil
				}},
			}})
		})
	}
}

func TestAccAppEnvironmentProtectedReplacementKeepsOldEnvironment(t *testing.T) {
	shrinkPollInterval(t)
	for _, field := range []string{"app", "name", "cluster"} {
		t.Run(field, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			base := testAccAppEnvironmentConfig(mock.url(), "a", "")
			protected := strings.Replace(base, "cluster = \"europe\"\n  deletion_protection = false", "cluster = \"europe\"\n  deletion_protection = true", 1)
			replacement := protected
			switch field {
			case "app":
				replacement = strings.Replace(replacement, "app = kupe_app.environment_parent.name", "app = \"another\"", 1)
			case "name":
				replacement = strings.Replace(replacement, "name = \"production\"", "name = \"staging\"", 1)
			case "cluster":
				replacement = strings.Replace(replacement, "cluster = \"europe\"", "cluster = \"elsewhere\"", 1)
			}
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: protected},
				{Config: replacement, ExpectError: regexp.MustCompile("(?s)deletion protection enabled.*deletion_protection = false")},
				{Config: base, Check: acc.TestCheckResourceAttr(appEnvironmentAddr, "name", "production")},
			}})
			if got := appEnvironmentRequestCount(mock, "POST", "/environments"); got != 1 {
				t.Fatalf("protected old environment was replaced: %d creates", got)
			}
		})
	}
}

func TestAccAppEnvironmentNamespaceGuardAndAppsOffCleanup(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := testAccAppEnvironmentConfig(mock.url(), "a", "")
	protected := strings.Replace(config, "cluster = \"europe\"\n  deletion_protection = false", "cluster = \"europe\"\n  deletion_protection = true", 1)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: protected},
		{Config: config, PreConfig: func() { mock.mu.Lock(); mock.appsDisabled = true; mock.mu.Unlock() }},
		{Config: config, Destroy: true, PreConfig: func() { mock.mu.Lock(); mock.appEnvironmentNamespaceInUse = true; mock.mu.Unlock() }, ExpectError: regexp.MustCompile("(?s)namespace is in use.*independent secret")},
		{Config: config, PreConfig: func() {
			mock.mu.Lock()
			mock.appEnvironmentNamespaceInUse = false
			mock.appEnvironmentDeleteLag = 2
			mock.mu.Unlock()
		}},
	}})
}

func TestAccAppEnvironmentForEachUsesIndependentETags(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := func(image string) string {
		return testAccProviderConfig(mock.url()) + fmt.Sprintf(`
resource "kupe_app" "parallel" {
 name = "parallel"
 deletion_protection = false
}
resource "kupe_app_environment" "parallel" {
 for_each = toset(["staging", "production"])
 app = kupe_app.parallel.name
 name = each.key
 cluster = "europe"
 deletion_protection = false
 web = {
   image = "ghcr.io/kupecloud/hello-kupe@sha256:%s"
   port = 8080
 }
}
`, strings.Repeat(image, 64))
	}
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config("a")},
		{Config: config("b"), PreConfig: func() {
			mock.mu.Lock()
			mock.appEnvironmentBeforePatch = []func(*client.AppEnvironment){func(env *client.AppEnvironment) { env.Generation++ }}
			mock.mu.Unlock()
		}},
		{Config: config("b"), Destroy: true},
	}})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	patches := 0
	for _, req := range mock.appEnvironmentRequests {
		if req.method == "PATCH" {
			patches++
			if req.etag == "" {
				t.Fatal("parallel patch lacked If-Match")
			}
		}
	}
	if patches != 3 {
		t.Fatalf("expected two writes plus one bounded stale retry, got %d PATCH requests", patches)
	}
}

func TestAccAppEnvironmentDeleteTimeoutWarnsAndContinues(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	mock.apps["shop"] = map[string]any{"name": "shop"}
	config := testAccAppEnvironmentConfig(mock.url(), "a", "timeouts = { delete = \"1s\" }")
	start := strings.Index(config, "resource \"kupe_app\"")
	end := strings.Index(config, "resource \"kupe_app_environment\"")
	config = config[:start] + config[end:]
	config = strings.Replace(config, "app = kupe_app.environment_parent.name", "app = \"shop\"", 1)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config},
		{Config: config, Destroy: true, PreConfig: func() { mock.mu.Lock(); mock.appEnvironmentDeleteLag = 100000; mock.mu.Unlock() }},
	}})
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.appEnvironments) != 1 || len(mock.appEnvironmentDeleting) != 1 {
		t.Fatal("timeout fixture did not leave cleanup in progress")
	}
}

func TestAccAppEnvironmentTagFailsAtPlan(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	config := testAccAppEnvironmentConfig(mock.url(), "a", "")
	config = strings.Replace(config, "@sha256:"+strings.Repeat("a", 64), ":latest", 1)
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{{Config: config, PlanOnly: true, ExpectError: regexp.MustCompile("(?s)Image digest required.*data.kupe_app_image")}}})
	if got := appEnvironmentRequestCount(mock, "POST", "/environments"); got != 0 {
		t.Fatal("invalid tag reached create")
	}
}

func TestAccAppEnvironmentUnavailableRefreshKeepsState(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	config := testAccAppEnvironmentConfig(mock.url(), "a", "")
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config},
		{Config: config, PlanOnly: true, PreConfig: func() { mock.mu.Lock(); mock.appEnvironmentReadsUnavailable = true; mock.mu.Unlock() }},
		{Config: config, PreConfig: func() { mock.mu.Lock(); mock.appEnvironmentReadsUnavailable = false; mock.mu.Unlock() }},
	}})
}

func TestAccAppEnvironmentNullEntriesFailPlanWithoutWrites(t *testing.T) {
	for _, listName := range []string{"secrets", "env"} {
		t.Run(listName, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			config := testAccAppEnvironmentConfig(mock.url(), "a", "")
			if listName == "secrets" {
				config = testAccAppEnvironmentConfig(mock.url(), "a", "secrets = [null]")
			} else {
				config = strings.Replace(config, "port = 8080", "port = 8080\n    env = [null]", 1)
			}
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: config, PlanOnly: true, ExpectError: regexp.MustCompile("(?s)Invalid nested entry.*List entries must be non-null objects")},
				{Config: testAccAppEnvironmentConfig(mock.url(), "a", ""), PlanOnly: true, ExpectNonEmptyPlan: true},
			}})
			mock.mu.Lock()
			defer mock.mu.Unlock()
			for _, request := range mock.appRequests {
				if request.method != "GET" {
					t.Fatalf("invalid nested entry caused an App write: %+v", request)
				}
			}
			for _, request := range mock.appEnvironmentRequests {
				if request.method != "GET" {
					t.Fatalf("invalid nested entry caused an environment write: %+v", request)
				}
			}
		})
	}
}

// The plan hook changes only background status after OpenTofu has produced
// the known plan. The next GET belongs to Update, not planning refresh.
type appAfterPlanHook func()

func (hook appAfterPlanHook) CheckPlan(_ context.Context, _ plancheck.CheckPlanRequest, _ *plancheck.CheckPlanResponse) {
	hook()
}

func TestAccAppEnvironmentTimeoutOnlyUpdatePreservesPlannedStatus(t *testing.T) {
	shrinkPollInterval(t)
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%v", recovery), func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			mock.appEnvironmentOutcome = "Deploying"
			if recovery {
				mock.appEnvironmentOutcome = "Failed"
				mock.appEnvironmentRecovery = true
				mock.appEnvironmentAfterWrite = []func(*client.AppEnvironment, *[]client.AppRelease){func(env *client.AppEnvironment, releases *[]client.AppRelease) {
					env.Status.Recovery.Phase = "Deploying"
					(*releases)[0].Status.Recovery.Phase = "Deploying"
				}}
			}
			before := testAccAppEnvironmentConfig(mock.url(), "a", "timeouts = { create = \"1s\", update = \"1s\" }")
			after := testAccAppEnvironmentConfig(mock.url(), "a", "timeouts = { create = \"1s\", update = \"2s\" }")
			transition := appAfterPlanHook(func() {
				mock.mu.Lock()
				defer mock.mu.Unlock()
				mock.appEnvironmentOnGet = []func(*client.AppEnvironment, *[]client.AppRelease){func(env *client.AppEnvironment, releases *[]client.AppRelease) {
					if recovery {
						env.Status.Recovery.Phase = "Succeeded"
						(*releases)[0].Status.Recovery.Phase = "Succeeded"
					} else {
						env.Status.Phase = "Running"
						env.Status.Deployment.Phase = "Succeeded"
						env.Status.ActiveRelease = env.Status.RequestedRelease
						env.Status.LastSuccessfulRelease = env.Status.RequestedRelease
						(*releases)[0].Status.Phase = "Succeeded"
					}
					mock.appEnvironmentOnGet = nil
				}}
			})
			assertNoWrites := func(*terraform.State) error {
				if got := appEnvironmentRequestCount(mock, "PATCH", "/production"); got != 0 {
					return fmt.Errorf("timeouts-only edit sent %d PATCH requests", got)
				}
				if got := appEnvironmentRequestCount(mock, "POST", "/retry"); got != 0 {
					return fmt.Errorf("timeouts-only edit sent %d retries", got)
				}
				return nil
			}
			acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
				{Config: before},
				{Config: after, ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(appEnvironmentAddr, plancheck.ResourceActionUpdate), transition}}, Check: assertNoWrites},
				{Config: after, PlanOnly: true},
				{Config: after, Check: assertNoWrites},
			}})
		})
	}
}

// Tag movement must change only the configured image. Computed deployment
// observations may be unknown until apply, so compare configured settings.
type appImageOnlyPlan struct{ before, after string }

func (check appImageOnlyPlan) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	found := false
	for _, change := range req.Plan.ResourceChanges {
		if change.Mode != "managed" {
			continue
		}
		if change.Address != appEnvironmentAddr {
			if !change.Change.Actions.NoOp() {
				resp.Error = fmt.Errorf("tag movement changed %s", change.Address)
				return
			}
			continue
		}
		found = true
		before, beforeOK := change.Change.Before.(map[string]any)
		after, afterOK := change.Change.After.(map[string]any)
		if !beforeOK || !afterOK {
			resp.Error = fmt.Errorf("environment settings missing from plan")
			return
		}
		beforeWeb, beforeOK := before["web"].(map[string]any)
		afterWeb, afterOK := after["web"].(map[string]any)
		if !beforeOK || !afterOK || beforeWeb["image"] != check.before || afterWeb["image"] != check.after {
			resp.Error = fmt.Errorf("plan did not contain the resolved image transition")
			return
		}
		for key, value := range beforeWeb {
			if key != "image" && !reflect.DeepEqual(value, afterWeb[key]) {
				resp.Error = fmt.Errorf("tag movement changed web.%s", key)
				return
			}
		}
		if len(beforeWeb) != len(afterWeb) {
			resp.Error = fmt.Errorf("tag movement changed the web settings shape")
			return
		}
		for _, key := range []string{"app", "name", "cluster", "deletion_protection", "deployment_revision", "secrets", "domains", "timeouts"} {
			if !reflect.DeepEqual(before[key], after[key]) {
				resp.Error = fmt.Errorf("tag movement changed %s", key)
				return
			}
		}
	}
	if !found {
		resp.Error = fmt.Errorf("environment missing from tag movement plan")
	}
}

func TestAccAppEnvironmentImageTagMovesOnceAndUnchangedApplyOnlyReads(t *testing.T) {
	shrinkPollInterval(t)
	mock := newMockKupeAPI()
	defer mock.close()
	first, next := mockAppImageResolution("a"), mockAppImageResolution("b")
	mock.appImages[mockAppImageTag] = first
	config := testAccAppEnvironmentConfig(mock.url(), "a", "")
	config = strings.Replace(config, fmt.Sprintf("image = %q", first.Reference), "image = data.kupe_app_image.web.reference", 1)
	config += fmt.Sprintf(`
data "kupe_app_image" "web" { image = %q }
`, mockAppImageTag)
	var writesBefore, resolutionsBefore int
	writeCount := func() int {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		count := 0
		for _, request := range mock.appRequests {
			if request.method != "GET" {
				count++
			}
		}
		for _, request := range mock.appEnvironmentRequests {
			if request.method != "GET" {
				count++
			}
		}
		return count
	}
	assertSingleImagePatch := func(*terraform.State) error {
		if got := appEnvironmentRequestCount(mock, "PATCH", "/production"); got != 1 {
			return fmt.Errorf("tag movement sent %d PATCH requests, want 1", got)
		}
		if got := appEnvironmentRequestCount(mock, "POST", "/retry"); got != 0 {
			return fmt.Errorf("tag movement sent %d retry requests", got)
		}
		if got := appEnvironmentRequestCount(mock, "POST", "/environments"); got != 1 {
			return fmt.Errorf("tag movement recreated the environment: %d creates", got)
		}
		mock.mu.Lock()
		defer mock.mu.Unlock()
		for _, request := range mock.appEnvironmentRequests {
			if request.method != "PATCH" {
				continue
			}
			web, ok := request.body["web"].(map[string]any)
			if !ok || web["image"] != next.Reference {
				return fmt.Errorf("tag movement PATCH did not deploy the newly resolved digest")
			}
		}
		return nil
	}
	acc.UnitTest(t, acc.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []acc.TestStep{
		{Config: config, Check: acc.TestCheckResourceAttr(appEnvironmentAddr, "web.image", first.Reference)},
		{Config: config, PreConfig: func() { mock.setState(func(m *mockKupeAPI) { m.appImages[mockAppImageTag] = next }) },
			ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(appEnvironmentAddr, plancheck.ResourceActionUpdate),
				appImageOnlyPlan{before: first.Reference, after: next.Reference},
			}},
			Check: acc.ComposeTestCheckFunc(acc.TestCheckResourceAttr(appEnvironmentAddr, "web.image", next.Reference), assertSingleImagePatch)},
		{Config: config, PreConfig: func() {
			writesBefore = writeCount()
			mock.mu.Lock()
			resolutionsBefore = len(mock.appImageRequests)
			mock.mu.Unlock()
		}, ConfigPlanChecks: acc.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: func(state *terraform.State) error {
			if got := writeCount(); got != writesBefore {
				return fmt.Errorf("unchanged apply wrote resources: %d -> %d", writesBefore, got)
			}
			mock.mu.Lock()
			resolutions := len(mock.appImageRequests)
			mock.mu.Unlock()
			if resolutions <= resolutionsBefore {
				return fmt.Errorf("unchanged apply used a cached image resolution")
			}
			return assertSingleImagePatch(state)
		}},
	}})
}
