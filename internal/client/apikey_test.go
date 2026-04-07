package client

import (
	"context"
	"net/http"
	"testing"
)

func TestListAPIKeys(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/tenants/acme/apikeys", http.StatusOK, map[string]any{
		"items": []APIKey{
			{ID: "ak-abc123", DisplayName: "CI/CD", Role: "admin"},
		},
	})

	c := mock.client("acme")
	keys, err := c.ListAPIKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1, got %d", len(keys))
	}
	if keys[0].ID != "ak-abc123" {
		t.Errorf("expected ak-abc123, got %q", keys[0].ID)
	}
}

func TestCreateAPIKey(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("POST", "/api/v1/tenants/acme/apikeys", http.StatusCreated, APIKey{
		ID:          "ak-new123",
		DisplayName: "Deploy Key",
		Role:        "admin",
		Key:         "kupe_new123_secretbytes",
		CreatedBy:   "admin@acme.com",
	})

	c := mock.client("acme")
	key, err := c.CreateAPIKey(context.Background(), CreateAPIKeyRequest{
		DisplayName: "Deploy Key",
		Role:        "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.Key != "kupe_new123_secretbytes" {
		t.Errorf("expected raw key, got %q", key.Key)
	}
	if key.ID != "ak-new123" {
		t.Errorf("expected ak-new123, got %q", key.ID)
	}
}

func TestDeleteAPIKey(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("DELETE", "/api/v1/tenants/acme/apikeys/ak-abc123", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	c := mock.client("acme")
	if err := c.DeleteAPIKey(context.Background(), "ak-abc123"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteAPIKey_NotFound(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	c := mock.client("acme")
	err := c.DeleteAPIKey(context.Background(), "ak-nonexistent")
	if !IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}
}
