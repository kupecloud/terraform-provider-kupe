package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestResolveAppImagePreservesIndexAndPlatformObjects(t *testing.T) {
	for _, input := range []string{"ghcr.io/kupecloud/hello-kupe:latest", "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("a", 64)} {
		t.Run(input, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			want := ImageResolution{Reference: "ghcr.io/kupecloud/hello-kupe@sha256:" + strings.Repeat("b", 64),
				Platforms: []ImagePlatform{{OS: "linux", Architecture: "arm64", Variant: "v8", Digest: "sha256:" + strings.Repeat("c", 64)}, {OS: "linux", Architecture: "amd64", Digest: "sha256:" + strings.Repeat("d", 64)}},
				User:      "65532:65532", ExposedPorts: []int{8080, 9090}}
			mock.onJSON(http.MethodPost, "/api/v1/tenants/acme/app-images/resolve", http.StatusOK, want)
			got, err := mock.client("acme").ResolveAppImage(context.Background(), input)
			if err != nil || !reflect.DeepEqual(got, &want) {
				t.Fatalf("resolution = %+v, error = %v; want %+v", got, err, want)
			}
			req := mock.lastRequest()
			var body map[string]string
			if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["image"] != input || req.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatal("resolve did not send the image with the existing API authentication")
			}
		})
	}
}

func TestResolveAppImageKeepsCodedErrorsAndRetryAfterWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{CodeImageNotFound, http.StatusNotFound},
		{CodeImageUnavailable, http.StatusServiceUnavailable},
		{CodeImagePlatformUnsupported, http.StatusBadRequest},
		{CodeImagePolicyDenied, http.StatusBadRequest},
		{CodeAppsUnavailable, http.StatusNotFound},
	} {
		t.Run(tc.code, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.on(http.MethodPost, "/api/v1/tenants/acme/app-images/resolve", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(tc.status)
				if err := json.NewEncoder(w).Encode(errorEnvelope{Code: tc.code, Message: "safe resolver refusal", Field: "image", Severity: "error"}); err != nil {
					t.Error(err)
				}
			})
			got, err := mock.client("acme").ResolveAppImage(context.Background(), "ghcr.io/kupecloud/hello-kupe:latest")
			var apiErr *APIError
			if got != nil || !errors.As(fmt.Errorf("outer: %w", err), &apiErr) || apiErr.Code != tc.code || apiErr.Message != "safe resolver refusal" || apiErr.RetryAfter != "17" || apiErr.Field != "image" {
				t.Fatalf("typed resolver refusal lost metadata: %v", err)
			}
			if len(mock.requests) != 1 {
				t.Fatalf("resolve POST retried %d times", len(mock.requests))
			}
		})
	}
}
