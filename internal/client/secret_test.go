package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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

// Pin the released API wire names independently of the Go response model.
func TestGetSecretDeliveryContract(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.on("GET", "/api/v1/tenants/acme/secrets/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"config","generation":4,"sync":[{"cluster":"prod","namespace":"app","keys":["password","username"]}],"status":{"phase":"Active","observedGeneration":3,"conditions":[{"type":"Ready","status":"False","reason":"Updating"}],"syncStatuses":[{"cluster":"prod","namespace":"app","secretName":"config","phase":"Pending","reason":"AwaitingCopy","deliveredRefresh":"ignored","lastDeliveredAt":"ignored"}]}}`))
	})
	s, _, err := mock.client("acme").GetSecret(context.Background(), "config")
	if err != nil {
		t.Fatal(err)
	}
	if s.Generation != 4 || s.Status == nil || s.Status.ObservedGeneration != 3 {
		t.Fatal("generation contract not decoded")
	}
	if len(s.Sync) != 1 || !slices.Equal(s.Sync[0].Keys, []string{"password", "username"}) {
		t.Fatal("keys contract not decoded")
	}
	if ConditionReason(s.Status.Conditions, "Ready") != "Updating" {
		t.Fatal("conditions contract not decoded")
	}
	if len(s.Status.SyncStatuses) != 1 || s.Status.SyncStatuses[0] != (SecretTargetStatus{Cluster: "prod", Namespace: "app", SecretName: "config", Phase: "Pending", Reason: "AwaitingCopy"}) {
		t.Fatal("target status contract not decoded")
	}
}

func TestSecretKeysRequestContract(t *testing.T) {
	for _, keys := range [][]string{nil, {"password", "username"}} {
		t.Run(fmt.Sprintf("%d keys", len(keys)), func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			for _, method := range []string{http.MethodPost, http.MethodPatch} {
				path := "/api/v1/tenants/acme/secrets"
				if method == http.MethodPatch {
					path += "/config"
				}
				mock.onJSON(method, path, http.StatusOK, Secret{Name: "config"})
			}
			c := mock.client("acme")
			targets := []SyncTarget{{Cluster: "prod", Namespace: "app", Keys: keys}}
			for _, method := range []string{http.MethodPost, http.MethodPatch} {
				var err error
				if method == http.MethodPost {
					_, _, err = c.CreateSecret(context.Background(), CreateSecretRequest{Name: "config", SecretPath: "app/config", Sync: targets})
				} else {
					_, _, err = c.UpdateSecret(context.Background(), "config", `"1"`, PatchSecretRequest{Sync: targets})
				}
				if err != nil {
					t.Fatal(err)
				}
				var body struct {
					Sync []map[string]json.RawMessage `json:"sync"`
				}
				if err := json.Unmarshal([]byte(mock.lastRequest().Body), &body); err != nil {
					t.Fatal(err)
				}
				if len(body.Sync) != 1 {
					t.Fatal("missing target")
				}
				value, present := body.Sync[0]["keys"]
				if present != (keys != nil) {
					t.Error("keys omission contract changed")
				}
				var got []string
				if present {
					if err := json.Unmarshal(value, &got); err != nil {
						t.Fatal(err)
					}
				}
				if !slices.Equal(got, keys) {
					t.Error("keys were merged or changed")
				}
			}
		})
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
