package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDatabaseAccessWirePresence(t *testing.T) {
	roles := []DeclaredRole{}
	body, err := json.Marshal(PatchDatabaseRequest{Postgres: &DatabasePostgres{Roles: &roles}})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"postgres":{"roles":[]}}` {
		t.Fatalf("clear roles body=%s", body)
	}
	body, err = json.Marshal(PatchDatabaseBindingRequest{Credentials: &BindingCredentials{Dedicated: true}})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"credentials":{"dedicated":true}}` {
		t.Fatalf("credentials-only body=%s", body)
	}
}

func TestRotateDatabaseBindingCredentialsContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/databases/orders/bindings/reporting/rotate-credentials") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, err := w.Write([]byte(`{"database":"orders","binding":"reporting","requestedAt":"opaque-token"}`))
		if err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := New(server.URL, "acme", "fake-test-key")
	response, err := c.RotateDatabaseBindingCredentials(context.Background(), "orders", "reporting")
	if err != nil {
		t.Fatal(err)
	}
	if response.RequestedAt != "opaque-token" {
		t.Fatalf("token=%q", response.RequestedAt)
	}
}
