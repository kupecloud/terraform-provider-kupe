package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetSecret(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/tenants/acme/secrets/db-pass", http.StatusOK, Secret{
		Name:       "db-pass",
		SecretPath: "prod/db-pass",
		Sync:       []SyncTarget{{Cluster: "prod", Namespace: "default"}},
		Status:     &SecretStatus{Phase: "Active"},
	})

	c := mock.client("acme")
	secret, etag, err := c.GetSecret(context.Background(), "db-pass")
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != "db-pass" {
		t.Errorf("expected db-pass, got %q", secret.Name)
	}
	if len(secret.Sync) != 1 {
		t.Fatalf("expected 1 sync target, got %d", len(secret.Sync))
	}
	if secret.Sync[0].Cluster != "prod" {
		t.Errorf("expected cluster=prod, got %q", secret.Sync[0].Cluster)
	}
	if etag == "" {
		t.Error("expected non-empty etag")
	}
}

func TestCreateSecret(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("POST", "/api/v1/tenants/acme/secrets", http.StatusCreated, Secret{
		Name:       "api-key",
		SecretPath: "prod/api-key",
	})

	c := mock.client("acme")
	secret, _, err := c.CreateSecret(context.Background(), CreateSecretRequest{
		Name:       "api-key",
		SecretPath: "prod/api-key",
		Sync:       []SyncTarget{{Cluster: "prod", Namespace: "app"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != "api-key" {
		t.Errorf("expected api-key, got %q", secret.Name)
	}
}

func TestUpdateSecret(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PATCH", "/api/v1/tenants/acme/secrets/db-pass", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != `"v1"` {
			t.Errorf("expected If-Match, got %q", r.Header.Get("If-Match"))
		}
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(Secret{Name: "db-pass"}); err != nil {
			t.Fatalf("failed to encode secret response: %v", err)
		}
	})

	c := mock.client("acme")
	_, etag, err := c.UpdateSecret(context.Background(), "db-pass", `"v1"`, PatchSecretRequest{
		Sync: []SyncTarget{{Cluster: "staging", Namespace: "default"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"v2"` {
		t.Errorf("expected v2, got %q", etag)
	}
}

func TestDeleteSecret(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("DELETE", "/api/v1/tenants/acme/secrets/db-pass", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	c := mock.client("acme")
	if err := c.DeleteSecret(context.Background(), "db-pass"); err != nil {
		t.Fatal(err)
	}
}
