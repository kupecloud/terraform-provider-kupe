package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const mockAppImageTag = "ghcr.io/kupecloud/hello-kupe:latest"

func mockAppImageResolution(digest string) client.ImageResolution {
	return client.ImageResolution{
		Reference: "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat(digest, 64),
		Platforms: []client.ImagePlatform{
			{OS: "linux", Architecture: "arm64", Variant: "v8", Digest: "sha256:" + strings.Repeat("c", 64)},
			{OS: "linux", Architecture: "amd64", Digest: "sha256:" + strings.Repeat("d", 64)},
		},
		User: "65532:65532", ExposedPorts: []int{8080, 9090},
	}
}

func TestAccAppImageDataSource(t *testing.T) {
	for _, input := range []string{mockAppImageTag, mockAppImageResolution("a").Reference} {
		t.Run(input, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			want := mockAppImageResolution("a")
			mock.setState(func(m *mockKupeAPI) { m.appImages[input] = want })
			const addr = "data.kupe_app_image.test"
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{
				Config: testAccProviderConfig(mock.url()) + fmt.Sprintf(`data "kupe_app_image" "test" { image = %q }`, input),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "image", input),
					resource.TestCheckResourceAttr(addr, "reference", want.Reference),
					resource.TestCheckResourceAttr(addr, "platforms.#", "2"),
					resource.TestCheckResourceAttr(addr, "platforms.0", "linux/arm64/v8"),
					resource.TestCheckResourceAttr(addr, "platforms.1", "linux/amd64"),
					resource.TestCheckResourceAttr(addr, "user", want.User),
					resource.TestCheckResourceAttr(addr, "exposed_ports.#", "2"),
					resource.TestCheckResourceAttr(addr, "exposed_ports.0", "8080"),
					resource.TestCheckResourceAttr(addr, "exposed_ports.1", "9090"),
				),
			}}})
		})
	}
}

func TestAccAppImageDataSourceBoundedPlatformsAndEmptyPorts(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	want := mockAppImageResolution("a")
	// A successful API response confirms amd64 support even when the bounded
	// summary contains only the first 16 other index manifests.
	want.Platforms = make([]client.ImagePlatform, 16)
	for i := range want.Platforms {
		want.Platforms[i] = client.ImagePlatform{OS: "linux", Architecture: "arm", Variant: fmt.Sprintf("v%d", i+1)}
	}
	want.ExposedPorts = nil
	mock.appImages[mockAppImageTag] = want
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{
		Config: testAccProviderConfig(mock.url()) + fmt.Sprintf(`data "kupe_app_image" "test" { image = %q }`, mockAppImageTag),
		Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("data.kupe_app_image.test", "reference", want.Reference),
			resource.TestCheckResourceAttr("data.kupe_app_image.test", "platforms.#", "16"),
			resource.TestCheckResourceAttr("data.kupe_app_image.test", "platforms.15", "linux/arm/v16"),
			resource.TestCheckResourceAttr("data.kupe_app_image.test", "exposed_ports.#", "0"),
		),
	}}})
}

var appImageErrorCases = []struct {
	code, summary, message string
	status                 int
}{
	{client.CodeImageNotFound, "app image not found", "image not found or not public", http.StatusNotFound},
	{client.CodeImageUnavailable, "app image temporarily unavailable", "the image is temporarily unavailable; retry in a few seconds", http.StatusServiceUnavailable},
	{client.CodeImagePlatformUnsupported, "app image platform unsupported", "the image must support linux/amd64", http.StatusBadRequest},
	{client.CodeImagePolicyDenied, "app image policy denied", "set a non-root numeric USER in the image, for example USER 101", http.StatusBadRequest},
	{client.CodeAppsUnavailable, "apps unavailable", "apps are not available on this platform", http.StatusNotFound},
}

func TestAccAppImageDataSourceCodedDiagnostics(t *testing.T) {
	for _, tc := range appImageErrorCases {
		t.Run(tc.code, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			mock.appImageRefusal = &mockRefusal{status: tc.status, code: tc.code, message: tc.message}
			mock.appImageRetryAfter = "17"
			messagePattern := strings.ReplaceAll(regexp.QuoteMeta(tc.message), " ", `\s+`)
			want := "(?s)" + regexp.QuoteMeta(tc.summary) + ".*" + messagePattern + ".*" + tc.code
			if tc.code == client.CodeImageUnavailable {
				want += ".*Retry after 17 seconds"
			}
			if tc.code == client.CodeAppsUnavailable {
				want += ".*Apps are not enabled"
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{
				Config:      testAccProviderConfig(mock.url()) + fmt.Sprintf(`data "kupe_app_image" "test" { image = %q }`, mockAppImageTag),
				ExpectError: regexp.MustCompile(want),
			}}})
		})
	}
}

func TestAppImageReadErrorsAreOnImage(t *testing.T) {
	for _, tc := range appImageErrorCases {
		for _, deferralAllowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deferral=%t", tc.code, deferralAllowed), func(t *testing.T) {
				mock := newMockKupeAPI()
				defer mock.close()
				mock.appImageRefusal = &mockRefusal{status: tc.status, code: tc.code, message: tc.message}
				resp := readAppImageForTest(t, mock, types.StringValue(mockAppImageTag), deferralAllowed)
				if resp.Deferred != nil || len(resp.Diagnostics.Errors()) != 1 {
					t.Fatalf("expected one non-deferred image error: %v", resp.Diagnostics)
				}
				got, ok := resp.Diagnostics.Errors()[0].(diag.DiagnosticWithPath)
				if !ok || !got.Path().Equal(path.Root("image")) || got.Summary() != tc.summary || !strings.Contains(got.Detail(), tc.message) {
					t.Fatalf("wrong image diagnostic: %v", resp.Diagnostics)
				}
				mock.mu.Lock()
				defer mock.mu.Unlock()
				if len(mock.appImageRequests) != 1 || mock.appImageRequests[0] != mockAppImageTag {
					t.Fatal("known-image refusal did not make exactly one resolver request")
				}
			})
		}
	}
}

func readAppImageForTest(t *testing.T, mock *mockKupeAPI, image types.String, deferralAllowed bool) datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	d := &AppImageDataSource{client: client.New(mock.url(), "acme", "test-token")}
	var sch datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sch)
	config := tfsdk.State{Schema: sch.Schema}
	if diags := config.Set(ctx, &appImageModel{
		Image: image, Reference: types.StringNull(), User: types.StringNull(),
		Platforms: types.ListNull(types.StringType), ExposedPorts: types.ListNull(types.Int64Type),
	}); diags.HasError() {
		t.Fatal(diags)
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sch.Schema, Raw: config.Raw.Copy()}}
	d.Read(ctx, datasource.ReadRequest{
		Config:             tfsdk.Config{Schema: sch.Schema, Raw: config.Raw},
		ClientCapabilities: datasource.ReadClientCapabilities{DeferralAllowed: deferralAllowed},
	}, &resp)
	return resp
}

func TestAppImageReadUnresolvedInputNeverCallsResolver(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		image                         types.String
		deferralAllowed, wantDeferred bool
	}{
		{name: "unknown with deferral", image: types.StringUnknown(), deferralAllowed: true, wantDeferred: true},
		{name: "unknown without deferral", image: types.StringUnknown()},
		{name: "null with deferral", image: types.StringNull(), deferralAllowed: true},
		{name: "null without deferral", image: types.StringNull()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockKupeAPI()
			defer mock.close()
			resp := readAppImageForTest(t, mock, tc.image, tc.deferralAllowed)
			mock.mu.Lock()
			requests := len(mock.appImageRequests)
			mock.mu.Unlock()
			if requests != 0 {
				t.Fatalf("unresolved input sent %d image requests", requests)
			}
			if tc.wantDeferred {
				if len(resp.Diagnostics) != 0 || resp.Deferred == nil || resp.Deferred.Reason != datasource.DeferredReasonDataSourceConfigUnknown {
					t.Fatalf("unknown input did not defer with config-unknown reason: %+v", resp)
				}
				var state appImageModel
				if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
					t.Fatal(diags)
				}
				if !state.Image.IsUnknown() || !state.Reference.IsUnknown() || !state.Platforms.IsUnknown() || !state.User.IsUnknown() || !state.ExposedPorts.IsUnknown() {
					t.Fatal("deferred read claimed known image metadata")
				}
				return
			}
			if resp.Deferred != nil || len(resp.Diagnostics.Errors()) != 1 {
				t.Fatalf("unresolved input needs one non-deferred diagnostic: %+v", resp)
			}
			got, ok := resp.Diagnostics.Errors()[0].(diag.DiagnosticWithPath)
			if !ok || !got.Path().Equal(path.Root("image")) || got.Summary() != "app image required" || got.Detail() != "Set `image` to an image tag or digest that is known when the data source is read." {
				t.Fatalf("unexpected unresolved image diagnostic: %v", resp.Diagnostics)
			}
		})
	}
}

func TestAppImageReadKnownInputResolvesWithEitherCapability(t *testing.T) {
	ctx := context.Background()
	want := mockAppImageResolution("a")
	for _, input := range []string{mockAppImageTag, want.Reference} {
		for _, deferralAllowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deferral=%t", input, deferralAllowed), func(t *testing.T) {
				mock := newMockKupeAPI()
				defer mock.close()
				mock.appImages[input] = want
				resp := readAppImageForTest(t, mock, types.StringValue(input), deferralAllowed)
				if len(resp.Diagnostics) != 0 || resp.Deferred != nil {
					t.Fatalf("known image did not read normally: %+v", resp)
				}
				var state appImageModel
				if diags := resp.State.Get(ctx, &state); diags.HasError() {
					t.Fatal(diags)
				}
				if state.Image.ValueString() != input || state.Reference.ValueString() != want.Reference || state.User.ValueString() != want.User || state.Platforms.IsUnknown() || state.ExposedPorts.IsUnknown() {
					t.Fatalf("known image metadata changed: %+v", state)
				}
				mock.mu.Lock()
				defer mock.mu.Unlock()
				if len(mock.appImageRequests) != 1 || mock.appImageRequests[0] != input {
					t.Fatal("known image did not make exactly one resolver request")
				}
			})
		}
	}
}

type appImageNoResolutionPlan struct{ mock *mockKupeAPI }

func (check appImageNoResolutionPlan) CheckPlan(_ context.Context, _ plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	check.mock.mu.Lock()
	defer check.mock.mu.Unlock()
	if len(check.mock.appImageRequests) != 0 {
		resp.Error = fmt.Errorf("dependency-derived unknown image reached the resolver during planning")
	}
}

func TestAccAppImageDataSourceDependencyKnownAtApply(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	want := mockAppImageResolution("a")
	mock.appImages[mockAppImageTag] = want
	const addr = "data.kupe_app_image.test"
	config := testAccProviderConfig(mock.url()) + fmt.Sprintf(`
resource "terraform_data" "image" { input = %q }
data "kupe_app_image" "test" { image = terraform_data.image.output }
`, mockAppImageTag)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(),
		ExternalProviders: map[string]resource.ExternalProvider{
			"terraform": {Source: "terraform.io/builtin/terraform"},
		},
		Steps: []resource.TestStep{{
			Config: config,
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectUnknownValue(addr, tfjsonpath.New("image")),
				plancheck.ExpectUnknownValue(addr, tfjsonpath.New("reference")),
				appImageNoResolutionPlan{mock: mock},
			}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(addr, "image", mockAppImageTag),
				resource.TestCheckResourceAttr(addr, "reference", want.Reference),
				func(*terraform.State) error {
					mock.mu.Lock()
					defer mock.mu.Unlock()
					if len(mock.appImageRequests) == 0 {
						return fmt.Errorf("apply did not resolve the known dependency image")
					}
					for _, input := range mock.appImageRequests {
						if input != mockAppImageTag {
							return fmt.Errorf("dependency image reached resolver before its value was known")
						}
					}
					return nil
				},
			),
		}},
	})
}

func TestAppImageDiagnosticsKeepURLsValuesAndTokensPrivate(t *testing.T) {
	const private = "https://private.example/secret-image?token=secret-value"
	for _, err := range []error{
		fmt.Errorf("request failed: %w", errors.New(private)),
		&client.APIError{StatusCode: 503, Message: private},
	} {
		summary, detail := appImageErrorDiagnostic(err)
		for _, value := range []string{"private.example", "secret-image", "secret-value"} {
			if strings.Contains(summary+detail, value) {
				t.Fatalf("diagnostic leaked %s", value)
			}
		}
	}
	for _, retry := range []string{"https://private.example/secret-value", "-2", "0", "4294967296"} {
		_, detail := appImageErrorDiagnostic(&client.APIError{StatusCode: 503, Code: client.CodeImageUnavailable, Message: "image resolution is temporarily unavailable", RetryAfter: retry})
		if !strings.Contains(detail, "after a short delay") || strings.Contains(detail, "Retry after ") || strings.Contains(detail, "private.example") {
			t.Fatalf("unsafe Retry-After hint: %s", detail)
		}
	}
}

func TestAccAppImageDataSourceRejectsMutableResolution(t *testing.T) {
	mock := newMockKupeAPI()
	defer mock.close()
	want := mockAppImageResolution("a")
	want.Reference = mockAppImageTag
	mock.appImages[mockAppImageTag] = want
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), Steps: []resource.TestStep{{
		Config:      testAccProviderConfig(mock.url()) + fmt.Sprintf(`data "kupe_app_image" "test" { image = %q }`, mockAppImageTag),
		ExpectError: regexp.MustCompile("did not return an immutable sha256 image reference"),
	}}})
}
