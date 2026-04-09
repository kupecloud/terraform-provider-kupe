package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		apiErr, ok := err.(*APIError)
		if !ok {
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
