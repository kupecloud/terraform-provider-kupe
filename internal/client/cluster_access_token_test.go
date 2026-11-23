package client

import (
	"context"
	"net/http"
	"testing"
)

const catPath = "/api/v1/tenants/acme/clusters/dev/access-tokens"

func TestClusterAccessTokenLifecycle(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	c := mock.client("acme")
	ctx := context.Background()
	const id = "cat-0a1b2c3d4e5f6a7b"
	mock.onJSON("POST", catPath, http.StatusCreated, map[string]any{
		"id": id, "cluster": "dev", "displayName": "ci", "access": map[string]any{"role": "admin"},
		"createdBy": "admin@acme.com", "createdAt": "2026-11-22T10:00:00Z", "expiresAt": "2027-02-20T10:00:00Z",
		"collected": false, "status": map[string]any{"phase": "Pending"},
	})
	mock.onJSON("GET", catPath+"/"+id, http.StatusOK, map[string]any{
		"id": id, "cluster": "dev", "displayName": "ci", "access": map[string]any{"role": "admin"},
		"createdBy": "admin@acme.com", "createdAt": "2026-11-22T10:00:00Z", "expiresAt": "2027-02-20T10:00:00Z",
		"collectedAt": "2026-11-22T10:00:05Z", "collected": true,
		"status": map[string]any{"phase": "Ready", "message": "", "conditions": []any{}},
	})
	mock.onJSON("POST", catPath+"/"+id+"/token", http.StatusOK, map[string]any{
		"id": id, "cluster": "dev", "token": "tok", "kubeconfig": "apiVersion: v1\n",
		"endpoint": "https://dev.acme.kupe.cloud", "certificateAuthority": "Q0E=",
		"expiresAt": "2027-02-20T10:00:00Z", "expirationTimestamp": "2027-02-20T10:00:00Z",
	})
	mock.on("DELETE", catPath+"/"+id, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	days := int64(30)
	created, err := c.CreateClusterAccessToken(ctx, "dev", CreateClusterAccessTokenRequest{
		DisplayName: "ci", Access: ClusterAccess{Role: "admin"}, ExpiresInDays: &days,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != id || created.Status.Phase != "Pending" || created.Collected || created.Access.Role != "admin" {
		t.Errorf("unexpected create response: %+v", created)
	}
	body := decodeBody(t, mock.lastRequest())
	access, _ := body["access"].(map[string]any)
	if body["expiresInDays"] != float64(30) || body["displayName"] != "ci" || access["role"] != "admin" || len(body) != 3 {
		t.Errorf("create body = %v, want displayName, access.role admin and expiresInDays 30", body)
	}

	// Without a lifetime the field is omitted, so the platform's default applies.
	if _, err := c.CreateClusterAccessToken(ctx, "dev", CreateClusterAccessTokenRequest{
		DisplayName: "ci", Access: ClusterAccess{Role: "admin"},
	}); err != nil {
		t.Fatal(err)
	}
	if body := decodeBody(t, mock.lastRequest()); body["expiresInDays"] != nil {
		t.Errorf("create body = %v, want expiresInDays omitted", body)
	}

	token, err := c.GetClusterAccessToken(ctx, "dev", id)
	if err != nil {
		t.Fatal(err)
	}
	if !token.Collected || token.CollectedAt != "2026-11-22T10:00:05Z" || token.Status.Phase != "Ready" || token.Cluster != "dev" {
		t.Errorf("unexpected token: %+v", token)
	}

	collected, err := c.CollectClusterAccessToken(ctx, "dev", id)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Token != "tok" || collected.Kubeconfig != "apiVersion: v1\n" || collected.Endpoint == "" {
		t.Errorf("unexpected collect response: token length %d, kubeconfig length %d, endpoint %q",
			len(collected.Token), len(collected.Kubeconfig), collected.Endpoint)
	}
	if req := mock.lastRequest(); req.Method != "POST" || req.Path != catPath+"/"+id+"/token" || req.Body != "" {
		t.Errorf("collect request = %s %s body %q, want POST with no body", req.Method, req.Path, req.Body)
	}

	if err := c.DeleteClusterAccessToken(ctx, "dev", id); err != nil {
		t.Fatal(err)
	}
}

// TestClusterAccessTokenCollectErrors: the collect's refusals arrive with
// their status and exact code, and only a 404 is not found.
func TestClusterAccessTokenCollectErrors(t *testing.T) {
	const id = "cat-0a1b2c3d4e5f6a7b"
	tests := []struct {
		status       int
		code         string
		wantNotFound bool
	}{
		{status: http.StatusConflict, code: CodeAccessTokenNotReady},
		{status: http.StatusConflict, code: CodeAccessTokenFailed},
		{status: http.StatusGone, code: CodeAccessTokenAlreadyCollected},
		{status: http.StatusForbidden, code: CodeAccessTokenCollectCreatorOnly},
		{status: http.StatusNotFound, wantNotFound: true},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status)+" "+tt.code, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.on("POST", catPath+"/"+id+"/token", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				if tt.code == "" {
					_, _ = w.Write([]byte(`{"error":"access token \"` + id + `\" not found"}`))
					return
				}
				_, _ = w.Write([]byte(`{"error":"refused","code":"` + tt.code + `","severity":"error","message":"refused"}`))
			})
			_, err := mock.client("acme").CollectClusterAccessToken(context.Background(), "dev", id)
			if err == nil {
				t.Fatal("CollectClusterAccessToken() error = nil")
			}
			if got := ErrorCode(err); got != tt.code {
				t.Errorf("ErrorCode() = %q, want %q", got, tt.code)
			}
			if got := IsNotFound(err); got != tt.wantNotFound {
				t.Errorf("IsNotFound() = %v, want %v", got, tt.wantNotFound)
			}
			if n := len(mock.requests); n != 1 {
				t.Errorf("collect sent %d requests, want 1 (a POST is never retried)", n)
			}
		})
	}
}

// TestClusterAccessTokenCodesExactCase pins the codes' spelling (kupe-api
// internal/errors/errors.go).
func TestClusterAccessTokenCodesExactCase(t *testing.T) {
	codes := map[string]string{
		CodeAccessTokenExpiryInvalid:      "ACCESS_TOKEN_EXPIRY_INVALID",
		CodeAccessTokenLimitReached:       "ACCESS_TOKEN_LIMIT_REACHED",
		CodeAccessTokenNotReady:           "ACCESS_TOKEN_NOT_READY",
		CodeAccessTokenFailed:             "ACCESS_TOKEN_FAILED",
		CodeAccessTokenAlreadyCollected:   "ACCESS_TOKEN_ALREADY_COLLECTED",
		CodeAccessTokenCollectCreatorOnly: "ACCESS_TOKEN_COLLECT_CREATOR_ONLY",
	}
	for got, want := range codes {
		if got != want {
			t.Errorf("code %q, want %q", got, want)
		}
	}
}
