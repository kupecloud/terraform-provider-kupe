package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestMain shrinks the transport retry backoff so tests that exercise the
// retry path (e.g. a persistent 5xx) don't sleep for real seconds. The
// retry COUNT is left at its production value so behaviour is unchanged.
func TestMain(m *testing.M) {
	retryWaitMin = time.Millisecond
	retryWaitMax = 5 * time.Millisecond
	os.Exit(m.Run())
}

// mockAPI creates a test HTTP server that records requests and returns configured responses.
type mockAPI struct {
	server   *httptest.Server
	requests []recordedRequest
	handlers map[string]http.HandlerFunc
}

type recordedRequest struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

func newMockAPI() *mockAPI {
	m := &mockAPI{
		handlers: make(map[string]http.HandlerFunc),
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0)
		if r.Body != nil {
			body, _ = readBody(r)
		}
		m.requests = append(m.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		key := r.Method + " " + r.URL.Path
		if handler, ok := m.handlers[key]; ok {
			handler(w, r)
			return
		}

		// Default: 404
		w.WriteHeader(http.StatusNotFound)
		if err := json.NewEncoder(w).Encode(ErrorResponse{Error: "not found"}); err != nil {
			panic(err)
		}
	}))
	return m
}

func (m *mockAPI) on(method, path string, handler http.HandlerFunc) {
	m.handlers[method+" "+path] = handler
}

func (m *mockAPI) onJSON(method, path string, status int, body any) {
	m.handlers[method+" "+path] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status == http.StatusOK || status == http.StatusCreated {
			w.Header().Set("ETag", `"12345"`)
		}
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			panic(err)
		}
	}
}

func (m *mockAPI) close() {
	m.server.Close()
}

func (m *mockAPI) client(tenant string) *Client {
	return New(m.server.URL, tenant, "test-token")
}

func (m *mockAPI) lastRequest() recordedRequest {
	if len(m.requests) == 0 {
		return recordedRequest{}
	}
	return m.requests[len(m.requests)-1]
}

func readBody(r *http.Request) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	buf := make([]byte, 0, 1024)
	for {
		tmp := make([]byte, 512)
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

func TestClientNew(t *testing.T) {
	c := New("https://api.kupe.cloud", "acme", "kupe_test_key")
	if c.baseURL != "https://api.kupe.cloud" {
		t.Errorf("expected baseURL, got %q", c.baseURL)
	}
	if c.tenant != "acme" {
		t.Errorf("expected tenant=acme, got %q", c.tenant)
	}
}

func TestClientTenantPath(t *testing.T) {
	c := New("https://api.kupe.cloud", "acme", "token")
	tests := []struct {
		segments []string
		want     string
	}{
		{nil, "/api/v1/tenants/acme"},
		{[]string{"clusters"}, "/api/v1/tenants/acme/clusters"},
		{[]string{"clusters", "prod"}, "/api/v1/tenants/acme/clusters/prod"},
	}
	for _, tt := range tests {
		got := c.tenantPath(tt.segments...)
		if got != tt.want {
			t.Errorf("tenantPath(%v) = %q, want %q", tt.segments, got, tt.want)
		}
	}
}

func TestClientAuthHeader(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/plans/starter", http.StatusOK, map[string]string{"name": "starter"})

	c := mock.client("acme")
	c.token = "my-secret-token"

	_, err := c.GetPlan(context.Background(), "starter")
	if err != nil {
		t.Fatal(err)
	}

	req := mock.lastRequest()
	if req.Header.Get("Authorization") != "Bearer my-secret-token" {
		t.Errorf("expected Bearer token, got %q", req.Header.Get("Authorization"))
	}
}

func TestClientErrorHandling(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/plans/missing", http.StatusNotFound, ErrorResponse{Error: "plan not found"})
	mock.onJSON("GET", "/api/v1/plans/forbidden", http.StatusForbidden, ErrorResponse{Error: "access denied"})

	c := mock.client("acme")

	t.Run("not found", func(t *testing.T) {
		_, err := c.GetPlan(context.Background(), "missing")
		if err == nil {
			t.Fatal("expected error")
		}
		if !IsNotFound(err) {
			t.Errorf("expected IsNotFound, got %v", err)
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		_, err := c.GetPlan(context.Background(), "forbidden")
		if err == nil {
			t.Fatal("expected error")
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("expected APIError, got %T", err)
		}
		if apiErr.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403, got %d", apiErr.StatusCode)
		}
	})
}

func TestIsNotFound(t *testing.T) {
	if IsNotFound(nil) {
		t.Error("nil should not be not-found")
	}
	if IsNotFound(&APIError{StatusCode: 500}) {
		t.Error("500 should not be not-found")
	}
	if !IsNotFound(&APIError{StatusCode: 404}) {
		t.Error("404 should be not-found")
	}
}

func TestIsConflict(t *testing.T) {
	if IsConflict(nil) {
		t.Error("nil should not be conflict")
	}
	if !IsConflict(&APIError{StatusCode: 409}) {
		t.Error("409 should be conflict")
	}
}

func TestAPIErrorMessage(t *testing.T) {
	err := &APIError{StatusCode: 400, Message: "bad request"}
	if err.Error() != "kupe api: 400 bad request" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

// TestRetryOnTransient5xx covers TPK-4: a GET that returns 503 twice then
// 200 must succeed transparently via the retrying transport rather than
// failing the first request. sync/atomic keeps the attempt counter safe
// even though the mock handler runs on the httptest server's goroutine.
func TestRetryOnTransient5xx(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	var attempts int32
	mock.on("GET", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"ok"`)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Cluster{Name: "prod"})
	})

	c := mock.client("acme")
	cluster, _, err := c.GetCluster(context.Background(), "prod")
	if err != nil {
		t.Fatalf("expected retry to recover, got error: %v", err)
	}
	if cluster.Name != "prod" {
		t.Errorf("expected prod, got %q", cluster.Name)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts (2 fail + 1 success), got %d", got)
	}
}

// TestNoRetryOnPost covers MEDIUM-2: a non-idempotent POST (here POST
// /apikeys) must NOT be retried by the transport. A retried POST after a
// committed-but-lost response would mint a second live credential the state
// never records. The handler always 503s; the client must give up after a
// single attempt and surface the error rather than retrying.
func TestNoRetryOnPost(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	var attempts int32
	mock.on("POST", "/api/v1/tenants/acme/apikeys", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	c := mock.client("acme")
	_, err := c.CreateAPIKey(context.Background(), CreateAPIKeyRequest{DisplayName: "ci", Role: "admin"})
	if err == nil {
		t.Fatal("expected error from persistent 503")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected exactly 1 attempt (POST must not retry), got %d", got)
	}
}

// TestRetryStillAppliesToIdempotentMethods guards that disabling POST retry
// did not disable retry for everyone: a transient 503 on a DELETE (an
// idempotent method) is still retried to recovery.
func TestRetryStillAppliesToIdempotentMethods(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	var attempts int32
	mock.on("DELETE", "/api/v1/tenants/acme/apikeys/ak-1", func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	c := mock.client("acme")
	if err := c.DeleteAPIKey(context.Background(), "ak-1"); err != nil {
		t.Fatalf("expected retry to recover DELETE, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts (2 fail + 1 success), got %d", got)
	}
}

// TestRedirectTreatedAsError covers MEDIUM-1: a 3xx response must surface
// as an error, never as a silent success. The client refuses to follow
// redirects (CheckRedirect returns ErrUseLastResponse), so requestWithETag
// receives the raw 3xx; without the 2xx-only success check a DELETE/PUT
// against a redirecting endpoint would return nil error and drop the
// resource from state while the server never processed the mutation.
func TestRedirectTreatedAsError(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently,  // 301
		http.StatusFound,             // 302
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect, // 308
	} {
		mock := newMockAPI()
		mock.on("DELETE", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://elsewhere.example.com/")
			w.WriteHeader(status)
		})

		c := mock.client("acme")
		err := c.DeleteCluster(context.Background(), "prod")
		if err == nil {
			mock.close()
			t.Fatalf("status %d: expected error, got nil (redirect silently treated as success)", status)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			mock.close()
			t.Fatalf("status %d: expected *APIError, got %T", status, err)
		}
		if apiErr.StatusCode != status {
			t.Errorf("status %d: expected APIError.StatusCode=%d, got %d", status, status, apiErr.StatusCode)
		}
		if apiErr.Message == "" {
			t.Errorf("status %d: expected non-empty message (status text fallback)", status)
		}
		mock.close()
	}
}

// TestNoRetryOn4xx confirms client errors are NOT retried (only the first
// attempt is made) — a 404 should surface immediately as a typed *APIError.
func TestNoRetryOn4xx(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	var attempts int32
	mock.on("GET", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "not found"})
	})

	c := mock.client("acme")
	_, _, err := c.GetCluster(context.Background(), "prod")
	if !IsNotFound(err) {
		t.Fatalf("expected not-found, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected exactly 1 attempt (no retry on 4xx), got %d", got)
	}
}
