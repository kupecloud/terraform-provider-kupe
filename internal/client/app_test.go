package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAppDeleteReviewsNoUnmanagedEnvironments(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	path := "/api/v1/tenants/acme/apps/shop"
	mock.onJSON(http.MethodDelete, path, http.StatusAccepted, map[string]any{"status": map[string]any{"phase": "Terminating"}})
	etag := `"opaque.uid.7"`
	if err := mock.client("acme").DeleteApp(context.Background(), "shop", etag); err != nil {
		t.Fatal(err)
	}
	req := mock.lastRequest()
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("If-Match") != etag || body["confirm"] != "shop" {
		t.Fatalf("delete lost its identity or ETag: %+v", req)
	}
	children, ok := body["environments"].([]any)
	if !ok || len(children) != 0 {
		t.Fatalf("expected explicit empty reviewed set, got %s", req.Body)
	}
}

func TestAppEnvironmentPatchSendsCompleteListsAndProbeRemoval(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	path := "/api/v1/tenants/acme/apps/shop/environments/staging"
	mock.onJSON(http.MethodPatch, path, http.StatusOK, map[string]any{"app": "shop", "environment": "staging"})
	emptySecrets, emptyDomains := []AppSecret{}, []string{}
	web := &AppWeb{Image: "registry.example/shop@sha256:" + strings.Repeat("a", 64), Port: 8080, Replicas: 1,
		Command: []string{}, Args: []string{}, Env: []AppEnvVar{},
		HealthChecks: AppHealthChecks{Readiness: &AppProbe{HTTP: &AppHTTPProbe{Path: "/ready"}}}}
	_, _, err := mock.client("acme").PatchAppEnvironment(context.Background(), "shop", "staging", `"env.uid.23"`, PatchAppEnvironmentRequest{Web: web, Secrets: &emptySecrets, Domains: &emptyDomains})
	if err != nil {
		t.Fatal(err)
	}
	req := mock.lastRequest()
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"secrets", "domains"} {
		if values, ok := body[key].([]any); !ok || len(values) != 0 {
			t.Errorf("%s must be [], got %s", key, req.Body)
		}
	}
	if _, present := body["deploymentRevision"]; present {
		t.Fatal("retry revision reached PATCH")
	}
	actualWeb := body["web"].(map[string]any)
	if values, ok := actualWeb["env"].([]any); !ok || len(values) != 0 {
		t.Fatalf("env was omitted or null: %s", req.Body)
	}
	checks := actualWeb["healthChecks"].(map[string]any)
	readiness := checks["readiness"].(map[string]any)
	if tcp, present := readiness["tcp"]; !present || tcp != nil {
		t.Fatalf("switching to HTTP must explicitly remove TCP: %s", req.Body)
	}
	if liveness, present := checks["liveness"]; !present || liveness != nil {
		t.Fatalf("clearing liveness must be explicit: %s", req.Body)
	}
	if req.Header.Get("If-Match") != `"env.uid.23"` {
		t.Fatal("opaque ETag changed")
	}
}

func TestAppEnvironmentRetryCarriesReturnedGenerationAndETag(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	path := "/api/v1/tenants/acme/apps/shop/environments/staging/retry"
	mock.on(http.MethodPost, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"env.uid.24"`)
		w.WriteHeader(http.StatusAccepted)
		if err := json.NewEncoder(w).Encode(AppRetryResponse{App: "shop", Environment: "staging", DeploymentRevision: 4, Generation: 24}); err != nil {
			t.Error(err)
		}
	})
	retry, etag, err := mock.client("acme").RetryAppEnvironment(context.Background(), "shop", "staging", `"env.uid.23"`)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Generation != 24 || retry.DeploymentRevision != 4 || etag != `"env.uid.24"` {
		t.Fatalf("wrong retry identity: %+v, %q", retry, etag)
	}
	if req := mock.lastRequest(); req.Body != "" || req.Header.Get("If-Match") != `"env.uid.23"` {
		t.Fatalf("wrong retry request: %+v", req)
	}
}

func TestAppsUnavailableIsNotResourceAbsence(t *testing.T) {
	err := &APIError{StatusCode: http.StatusNotFound, Code: CodeAppsUnavailable}
	if IsNotFound(err) {
		t.Fatal("apps_unavailable must never remove an existing resource from state")
	}
	if !IsNotFound(&APIError{StatusCode: http.StatusNotFound, Message: "app not found"}) {
		t.Fatal("ordinary absence must remain not found")
	}
}

func TestAppMutationsRefuseMissingETagWithoutSending(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	c := mock.client("acme")
	ctx := context.Background()
	checks := []func() error{
		func() error { _, _, err := c.PatchApp(ctx, "shop", "", PatchAppRequest{}); return err },
		func() error { return c.DeleteApp(ctx, "shop", "") },
		func() error {
			_, _, err := c.PatchAppEnvironment(ctx, "shop", "production", "", PatchAppEnvironmentRequest{})
			return err
		},
		func() error { return c.DeleteAppEnvironment(ctx, "shop", "production", "") },
		func() error { _, _, err := c.RetryAppEnvironment(ctx, "shop", "production", ""); return err },
	}
	for _, check := range checks {
		if err := check(); err == nil {
			t.Fatal("missing ETag accepted")
		}
	}
	if len(mock.requests) != 0 {
		t.Fatal("missing ETag caused a blind network mutation")
	}
}

func TestAppEnvironmentDeleteUsesEnvironmentConfirmationAndOpaqueETag(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON(http.MethodDelete, "/api/v1/tenants/acme/apps/shop/environments/production", http.StatusAccepted, map[string]any{"status": map[string]any{"phase": "Terminating"}})
	etag := `"incarnation.with.dots.19"`
	if err := mock.client("acme").DeleteAppEnvironment(context.Background(), "shop", "production", etag); err != nil {
		t.Fatal(err)
	}
	req := mock.lastRequest()
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["confirm"] != "production" || len(body) != 1 || req.Header.Get("If-Match") != etag {
		t.Fatalf("unsafe environment delete: %+v", req)
	}
}

func TestAppEnvironmentReadMapsPublicFlatStatus(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON(http.MethodGet, "/api/v1/tenants/acme/apps/shop/environments/production", http.StatusOK, map[string]any{
		"uid": "env-uid", "generation": 9, "app": "shop", "environment": "production", "cluster": "europe", "deploymentRevision": 3,
		"status": map[string]any{"observedGeneration": 9, "phase": "Degraded", "requestedRelease": map[string]any{"name": "request-3"}, "deployment": map[string]any{"release": map[string]any{"name": "request-3"}, "phase": "Failed", "reason": "SyncFailed"}, "recovery": map[string]any{"phase": "Succeeded", "release": map[string]any{"name": "serving-2"}}},
	})
	env, _, err := mock.client("acme").GetAppEnvironment(context.Background(), "shop", "production")
	if err != nil {
		t.Fatal(err)
	}
	if env.UID != "env-uid" || env.Generation != 9 || env.DeploymentRevision != 3 || env.Status == nil || env.Status.Deployment == nil || env.Status.Deployment.Phase != "Failed" || env.Status.Recovery == nil || env.Status.Recovery.Release.Name != "serving-2" {
		t.Fatalf("wrong public environment mapping: %+v", env)
	}
}
