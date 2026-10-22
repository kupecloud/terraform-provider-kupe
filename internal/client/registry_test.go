package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const registryPath = "/api/v1/tenants/acme/registries"

func decodeBody(t *testing.T, req recordedRequest) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("decoding %s %s body %q: %v", req.Method, req.Path, req.Body, err)
	}
	return body
}

func TestGetRegistry(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("GET", registryPath+"/registry", http.StatusOK, map[string]any{
		"name": "registry", "storageLimit": "10Gi", "deletionProtection": true, "generation": 2,
		"status": map[string]any{
			"observedGeneration": 2, "phase": "Running", "endpoint": "registry.kupe.cloud/acme",
			"storageUsedBytes": 1024, "storageLimitBytes": 10737418240, "repositoryCount": 3,
		},
		"resourceVersion": "7", "createdAt": "2026-10-21T10:00:00Z",
	})

	reg, etag, err := mock.client("acme").GetRegistry(context.Background(), "registry")
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"12345"` {
		t.Errorf("etag = %q", etag)
	}
	if reg.StorageLimit != "10Gi" || !reg.DeletionProtection || reg.Generation != 2 {
		t.Errorf("unexpected registry: %+v", reg)
	}
	if reg.Status.Endpoint != "registry.kupe.cloud/acme" || reg.Status.StorageLimitBytes == nil ||
		*reg.Status.StorageLimitBytes != 10737418240 || reg.Status.RepositoryCount != 3 {
		t.Errorf("unexpected status: %+v", reg.Status)
	}
}

func TestListRegistries(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("GET", registryPath, http.StatusOK, map[string]any{"items": []any{map[string]any{"name": "registry"}}})

	regs, err := mock.client("acme").ListRegistries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0].Name != "registry" {
		t.Errorf("ListRegistries() = %+v", regs)
	}
}

func TestCreateRegistry_Body(t *testing.T) {
	off := false
	tests := []struct {
		name string
		req  CreateRegistryRequest
		want map[string]any
	}{
		{name: "unset fields are omitted", req: CreateRegistryRequest{}, want: map[string]any{}},
		{
			name: "explicit false protection and a limit are sent",
			req:  CreateRegistryRequest{Name: "registry", StorageLimit: "10Gi", DeletionProtection: &off},
			want: map[string]any{"name": "registry", "storageLimit": "10Gi", "deletionProtection": false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.onJSON("POST", registryPath, http.StatusCreated, map[string]any{"name": "registry"})
			if _, _, err := mock.client("acme").CreateRegistry(context.Background(), tt.req); err != nil {
				t.Fatal(err)
			}
			body := decodeBody(t, mock.lastRequest())
			if len(body) != len(tt.want) {
				t.Fatalf("body = %v, want %v", body, tt.want)
			}
			for k, v := range tt.want {
				if body[k] != v {
					t.Errorf("body[%q] = %v, want %v", k, body[k], v)
				}
			}
		})
	}
}

func TestUpdateRegistry_ClearsLimitWithEmptyString(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("PATCH", registryPath+"/registry", http.StatusOK, map[string]any{"name": "registry"})
	none := ""
	if _, _, err := mock.client("acme").UpdateRegistry(context.Background(), "registry", `"5"`,
		PatchRegistryRequest{StorageLimit: &none}); err != nil {
		t.Fatal(err)
	}
	req := mock.lastRequest()
	if got := req.Header.Get("If-Match"); got != `"5"` {
		t.Errorf("If-Match = %q", got)
	}
	body := decodeBody(t, req)
	if v, ok := body["storageLimit"]; !ok || v != "" || len(body) != 1 {
		t.Errorf("PATCH body = %v, want only storageLimit=\"\"", body)
	}
}

func TestDeleteRegistry_DeletionProtected(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("DELETE", registryPath+"/registry", http.StatusConflict, map[string]any{
		"error": `registry "registry" has deletionProtection enabled`, "code": CodeDeletionProtected,
		"severity": "error", "message": `registry "registry" has deletionProtection enabled`,
	})
	if err := mock.client("acme").DeleteRegistry(context.Background(), "registry"); !IsDeletionProtected(err) {
		t.Fatalf("IsDeletionProtected(%v) = false, want true", err)
	}
}

func TestRegistryBindingLifecycle(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	c := mock.client("acme")
	ctx := context.Background()
	binding := map[string]any{
		"name": "prod", "registry": "registry", "cluster": "prod", "pushNamespaces": []any{"ci"},
		"secretName": "kupe-registry-push",
		"status": map[string]any{
			"phase": "Ready", "pullUsername": "kupe+acme+pull-prod", "pushUsername": "kupe+acme+push-prod",
			"namespaces": []any{map[string]any{"name": "ci", "phase": "Ready"}},
		},
	}
	mock.onJSON("POST", registryPath+"/registry/bindings", http.StatusCreated, binding)
	mock.onJSON("GET", registryPath+"/registry/bindings/prod", http.StatusOK, binding)
	mock.onJSON("PATCH", registryPath+"/registry/bindings/prod", http.StatusOK, binding)
	mock.on("DELETE", registryPath+"/registry/bindings/prod", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if _, _, err := c.CreateRegistryBinding(ctx, "registry", CreateRegistryBindingRequest{Cluster: "prod"}); err != nil {
		t.Fatal(err)
	}
	if body := decodeBody(t, mock.lastRequest()); len(body) != 1 || body["cluster"] != "prod" {
		t.Errorf("create body = %v, want only cluster (name, pushNamespaces and secretName omitted)", body)
	}
	got, _, err := c.GetRegistryBinding(ctx, "registry", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.PushUsername != "kupe+acme+push-prod" || len(got.Status.Namespaces) != 1 {
		t.Errorf("unexpected binding: %+v", got.Status)
	}
	// An empty list reaches the wire as [], never null: [] removes every push
	// namespace, null is refused.
	if _, _, err := c.UpdateRegistryBinding(ctx, "registry", "prod", `"3"`, PatchRegistryBindingRequest{}); err != nil {
		t.Fatal(err)
	}
	if req := mock.lastRequest(); req.Body != `{"pushNamespaces":[]}`+"\n" && req.Body != `{"pushNamespaces":[]}` {
		t.Errorf("PATCH body = %q, want {\"pushNamespaces\":[]}", req.Body)
	} else if req.Header.Get("If-Match") != `"3"` {
		t.Errorf("If-Match = %q", req.Header.Get("If-Match"))
	}
	if err := c.DeleteRegistryBinding(ctx, "registry", "prod"); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryTokenLifecycle(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	c := mock.client("acme")
	ctx := context.Background()
	mock.onJSON("POST", registryPath+"/registry/tokens", http.StatusCreated, map[string]any{
		"id": "rt-0a1b2c3d4e5f6a7b", "displayName": "ci", "kind": "token", "access": "push",
		"registry": "registry.kupe.cloud", "username": "kupe+acme+tok-0a1b2c3d4e5f6a7b",
		"secret": "kupecr_test", "status": map[string]any{"phase": ""},
	})
	mock.onJSON("GET", registryPath+"/registry/tokens/rt-0a1b2c3d4e5f6a7b", http.StatusOK, map[string]any{
		"id": "rt-0a1b2c3d4e5f6a7b", "username": "kupe+acme+tok-0a1b2c3d4e5f6a7b",
		"lastUsedAt": "2026-10-21T11:00:00Z", "status": map[string]any{"phase": "Ready"},
	})
	mock.on("DELETE", registryPath+"/registry/tokens/rt-0a1b2c3d4e5f6a7b", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	days := int64(30)
	created, err := c.CreateRegistryToken(ctx, "registry", CreateRegistryTokenRequest{
		DisplayName: "ci", Access: "push", ExpiresInDays: &days,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret != "kupecr_test" || created.Registry != "registry.kupe.cloud" || created.ID != "rt-0a1b2c3d4e5f6a7b" {
		t.Errorf("unexpected create response: id %q registry %q secret length %d", created.ID, created.Registry, len(created.Secret))
	}
	body := decodeBody(t, mock.lastRequest())
	if body["expiresInDays"] != float64(30) || body["displayName"] != "ci" || body["access"] != "push" {
		t.Errorf("create body = %v, want expiresInDays 30", body)
	}

	// Without a lifetime the field is omitted, so the platform's default applies.
	if _, err := c.CreateRegistryToken(ctx, "registry", CreateRegistryTokenRequest{DisplayName: "ci", Access: "pull"}); err != nil {
		t.Fatal(err)
	}
	if body := decodeBody(t, mock.lastRequest()); body["expiresInDays"] != nil {
		t.Errorf("create body = %v, want expiresInDays omitted", body)
	}

	token, err := c.GetRegistryToken(ctx, "registry", "rt-0a1b2c3d4e5f6a7b")
	if err != nil {
		t.Fatal(err)
	}
	if token.LastUsedAt != "2026-10-21T11:00:00Z" || token.Status.Phase != "Ready" {
		t.Errorf("unexpected token: %+v", token)
	}
	if err := c.DeleteRegistryToken(ctx, "registry", "rt-0a1b2c3d4e5f6a7b"); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryErrors(t *testing.T) {
	tests := []struct {
		name                                string
		status                              int
		contentType, body                   string
		wantNotFound, wantUnavailable       bool
		wantNotServed                       bool
		wantCode                            string
		wantDeletionProtected, wantConflict bool
	}{
		{
			name: "a named 404 is not found", status: http.StatusNotFound,
			body:         `{"error":"registry \"registry\" not found"}`,
			wantNotFound: true,
		},
		{
			name: "a named 404 from the platform is not found", status: http.StatusNotFound,
			body:         `{"error":"access token \"rt-0a1b2c3d4e5f6a7b\" not found"}`,
			wantNotFound: true,
		},
		{
			name: "switched off is not not found", status: http.StatusNotFound,
			body:            `{"error":"registries are not available on this platform","code":"registries_unavailable","severity":"error","message":"registries are not available on this platform"}`,
			wantUnavailable: true, wantCode: CodeRegistriesUnavailable,
		},
		{
			name: "the router's 404 of an API without registries is not served", status: http.StatusNotFound,
			contentType: "text/plain; charset=utf-8", body: "404 page not found\n",
			wantNotServed: true,
		},
		{
			name: "an unnamed JSON 404 is not served", status: http.StatusNotFound,
			body:          `{"error":"not found"}`,
			wantNotServed: true,
		},
		{
			name: "codes are kept exactly", status: http.StatusConflict,
			body:     `{"error":"this tenant already has a registry","code":"REGISTRY_ALREADY_EXISTS","severity":"error","message":"this tenant already has a registry"}`,
			wantCode: CodeRegistryAlreadyExists, wantConflict: true,
		},
		{
			name: "deletion protection", status: http.StatusConflict,
			body:     `{"error":"protected","code":"deletion_protected","severity":"error","message":"protected"}`,
			wantCode: CodeDeletionProtected, wantDeletionProtected: true, wantConflict: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.on("GET", registryPath+"/registry", func(w http.ResponseWriter, _ *http.Request) {
				ct := tt.contentType
				if ct == "" {
					ct = "application/json"
				}
				w.Header().Set("Content-Type", ct)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, _, err := mock.client("acme").GetRegistry(context.Background(), "registry")
			if err == nil {
				t.Fatal("GetRegistry() error = nil")
			}
			if got := IsNotFound(err); got != tt.wantNotFound {
				t.Errorf("IsNotFound() = %v, want %v (%v)", got, tt.wantNotFound, err)
			}
			if got := IsRegistriesUnavailable(err); got != tt.wantUnavailable {
				t.Errorf("IsRegistriesUnavailable() = %v, want %v", got, tt.wantUnavailable)
			}
			if got := IsRegistriesNotServed(err); got != tt.wantNotServed {
				t.Errorf("IsRegistriesNotServed() = %v, want %v", got, tt.wantNotServed)
			}
			if got := ErrorCode(err); got != tt.wantCode {
				t.Errorf("ErrorCode() = %q, want %q", got, tt.wantCode)
			}
			if got := IsDeletionProtected(err); got != tt.wantDeletionProtected {
				t.Errorf("IsDeletionProtected() = %v, want %v", got, tt.wantDeletionProtected)
			}
			if got := IsConflict(err); got != tt.wantConflict {
				t.Errorf("IsConflict() = %v, want %v", got, tt.wantConflict)
			}
			if tt.wantNotServed && !strings.Contains(err.Error(), "does not serve registries") {
				t.Errorf("error %q does not say the API does not serve registries", err)
			}
		})
	}
}

// TestRegistryCodesExactCase pins the codes' spelling: the provider compares
// codes exactly, and kupe-api's are lower case while the platform's are upper
// case.
func TestRegistryCodesExactCase(t *testing.T) {
	codes := map[string]string{
		CodeRegistriesUnavailable:              "registries_unavailable",
		CodeRegistryAlreadyExists:              "REGISTRY_ALREADY_EXISTS",
		CodeRegistryStorageLimitAbovePlan:      "REGISTRY_STORAGE_LIMIT_ABOVE_PLAN",
		CodeRegistryBindingExists:              "REGISTRY_BINDING_EXISTS",
		CodeRegistryPushNamespacesLimit:        "REGISTRY_PUSH_NAMESPACES_LIMIT",
		CodeRegistryPushNamespacesServiceLimit: "REGISTRY_PUSH_NAMESPACES_SERVICE_LIMIT",
		CodeRegistryTokenPushAdminOnly:         "REGISTRY_TOKEN_PUSH_ADMIN_ONLY",
		CodeRegistryTokenExpiryInvalid:         "REGISTRY_TOKEN_EXPIRY_INVALID",
		CodeRegistryTokenLimitReached:          "REGISTRY_TOKEN_LIMIT_REACHED",
	}
	for got, want := range codes {
		if got != want {
			t.Errorf("code %q, want %q", got, want)
		}
	}
	// A lower-cased platform code is not the code.
	err := &APIError{StatusCode: http.StatusConflict, Code: strings.ToLower(CodeRegistryAlreadyExists)}
	if ErrorCode(err) == CodeRegistryAlreadyExists {
		t.Error("a lower-cased code compared equal")
	}
}

func TestRegistryPathsEscapeSegments(t *testing.T) {
	c := New("http://localhost", "acme", "test-token")
	if got := c.tenantPath("registries", "registry", "tokens", "a/b"); got != registryPath+"/registry/tokens/a%2Fb" {
		t.Errorf("tenantPath = %q, want the id escaped", got)
	}
}
