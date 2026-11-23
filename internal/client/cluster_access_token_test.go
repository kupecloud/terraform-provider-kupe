package client

import (
	"context"
	"errors"
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
		CodeAccessTokenNotFound:           "ACCESS_TOKEN_NOT_FOUND",
		CodeClusterNotFound:               "CLUSTER_NOT_FOUND",
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

// TestRequestDelivered: a refusal (4xx) did nothing; a success, a 5xx and a
// transport error may have.
func TestRequestDelivered(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		want bool
	}{
		"success":   {nil, true},
		"409":       {&APIError{StatusCode: 409}, false},
		"410":       {&APIError{StatusCode: 410}, false},
		"404":       {&ClusterAccessTokensNotServedError{Err: &APIError{StatusCode: 404}}, false},
		"500":       {&APIError{StatusCode: 500}, true},
		"transport": {errors.New("connection reset"), true},
	} {
		if got := RequestDelivered(tt.err); got != tt.want {
			t.Errorf("%s: RequestDelivered() = %v, want %v", name, got, tt.want)
		}
	}
}

// TestClusterAccessTokenNotFoundKinds: only kupe-api's named 404 (the token
// or its cluster) is not found; a 404 that carries no code and names nothing
// is a route the API does not serve, which must never read as revoked.
func TestClusterAccessTokenNotFoundKinds(t *testing.T) {
	const id = "cat-0a1b2c3d4e5f6a7b"
	tests := []struct {
		name, contentType, body     string
		wantNotFound, wantNotServed bool
	}{
		{name: "token not found", body: `{"error":"access token \"` + id + `\" not found"}`, wantNotFound: true},
		{name: "cluster not found", body: `{"error":"cluster \"dev\" not found"}`, wantNotFound: true},
		{name: "coded token", body: `{"error":"gone","code":"ACCESS_TOKEN_NOT_FOUND"}`, wantNotFound: true},
		{name: "coded cluster", body: `{"error":"gone","code":"CLUSTER_NOT_FOUND"}`, wantNotFound: true},
		{name: "uncoded other token", body: `{"error":"access token \"cat-ffffffffffffffff\" not found"}`, wantNotServed: true},
		{name: "uncoded other cluster", body: `{"error":"cluster \"prod\" not found"}`, wantNotServed: true},
		{name: "unknown code", body: `{"error":"gone","code":"SOMETHING_ELSE"}`, wantNotServed: true},
		{name: "router 404", contentType: "text/plain; charset=utf-8", body: "404 page not found\n", wantNotServed: true},
		{name: "unnamed JSON 404", body: `{"error":"not found"}`, wantNotServed: true},
	}
	calls := map[string]func(*Client) error{
		"get": func(c *Client) error { _, err := c.GetClusterAccessToken(context.Background(), "dev", id); return err },
		"collect": func(c *Client) error {
			_, err := c.CollectClusterAccessToken(context.Background(), "dev", id)
			return err
		},
		"delete": func(c *Client) error { return c.DeleteClusterAccessToken(context.Background(), "dev", id) },
	}
	for _, tt := range tests {
		for call, do := range calls {
			t.Run(tt.name+"/"+call, func(t *testing.T) {
				mock := newMockAPI()
				defer mock.close()
				answer := func(w http.ResponseWriter, _ *http.Request) {
					ct := tt.contentType
					if ct == "" {
						ct = "application/json"
					}
					w.Header().Set("Content-Type", ct)
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(tt.body))
				}
				mock.on("GET", catPath+"/"+id, answer)
				mock.on("POST", catPath+"/"+id+"/token", answer)
				mock.on("DELETE", catPath+"/"+id, answer)
				err := do(mock.client("acme"))
				if err == nil {
					t.Fatal("error = nil")
				}
				if got := IsNotFound(err); got != tt.wantNotFound {
					t.Errorf("IsNotFound() = %v, want %v (%v)", got, tt.wantNotFound, err)
				}
				if got := IsClusterAccessTokensNotServed(err); got != tt.wantNotServed {
					t.Errorf("IsClusterAccessTokensNotServed() = %v, want %v (%v)", got, tt.wantNotServed, err)
				}
			})
		}
	}
}
