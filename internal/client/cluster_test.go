package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestListClusters(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/tenants/acme/clusters", http.StatusOK, map[string]any{
		"items": []Cluster{
			{Name: "prod", DisplayName: "Production", Type: "shared"},
			{Name: "staging", DisplayName: "Staging", Type: "shared"},
		},
	})

	c := mock.client("acme")
	clusters, err := c.ListClusters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
	if clusters[0].Name != "prod" {
		t.Errorf("expected prod, got %q", clusters[0].Name)
	}
}

func TestGetCluster(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/tenants/acme/clusters/prod", http.StatusOK, Cluster{
		Name:        "prod",
		DisplayName: "Production",
		Type:        "shared",
		Version:     "1.31",
		Status:      &ClusterStatus{Phase: "Running", Endpoint: "https://prod.local"},
	})

	c := mock.client("acme")
	cluster, etag, err := c.GetCluster(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if cluster.Name != "prod" {
		t.Errorf("expected prod, got %q", cluster.Name)
	}
	if cluster.Status.Phase != "Running" {
		t.Errorf("expected Running, got %q", cluster.Status.Phase)
	}
	if etag != `"12345"` {
		t.Errorf("expected ETag, got %q", etag)
	}
}

func TestCreateCluster(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("POST", "/api/v1/tenants/acme/clusters", http.StatusCreated, Cluster{
		Name:        "staging",
		DisplayName: "Staging",
		Type:        "shared",
	})

	c := mock.client("acme")
	cluster, _, err := c.CreateCluster(context.Background(), CreateClusterRequest{
		Name:        "staging",
		DisplayName: "Staging",
		Type:        "shared",
		Resources:   &ClusterResource{CPU: "2", Memory: "8Gi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cluster.Name != "staging" {
		t.Errorf("expected staging, got %q", cluster.Name)
	}

	// Verify request body was sent correctly
	req := mock.lastRequest()
	if req.Method != "POST" {
		t.Errorf("expected POST, got %s", req.Method)
	}

	var body CreateClusterRequest
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}
	if body.Resources.CPU != "2" {
		t.Errorf("expected CPU=2, got %q", body.Resources.CPU)
	}
}

func TestUpdateCluster(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PATCH", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, r *http.Request) {
		// Verify If-Match header
		if r.Header.Get("If-Match") != `"old-etag"` {
			t.Errorf("expected If-Match header, got %q", r.Header.Get("If-Match"))
		}
		w.Header().Set("ETag", `"new-etag"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(Cluster{Name: "prod", Version: "1.32"}); err != nil {
			t.Fatalf("failed to encode cluster response: %v", err)
		}
	})

	c := mock.client("acme")
	v := "1.32"
	cluster, etag, err := c.UpdateCluster(context.Background(), "prod", `"old-etag"`, PatchClusterRequest{
		Version: &v,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cluster.Version != "1.32" {
		t.Errorf("expected version 1.32, got %q", cluster.Version)
	}
	if etag != `"new-etag"` {
		t.Errorf("expected new-etag, got %q", etag)
	}
}

// TestUpdateCluster_OmitsVersionWhenNil is the wire-level guard for TPK-1:
// when PatchClusterRequest.Version is nil the marshalled PATCH body must
// NOT contain a "version" key. A pointer-to-"" would serialise to
// {"version":""}, which kupe-api resolves to a default version and could
// silently change the tenant cluster's Kubernetes minor version on an
// unrelated edit. The provider Update path only sets Version when the
// planned value is known and changed; this test pins the serialisation
// contract the omission relies on.
func TestUpdateCluster_OmitsVersionWhenNil(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PATCH", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"new-etag"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Cluster{Name: "prod", Version: "1.31"})
	})

	c := mock.client("acme")
	// Only resources changes; Version stays nil (the "version unset, edit
	// an unrelated field" scenario).
	_, _, err := c.UpdateCluster(context.Background(), "prod", `"old-etag"`, PatchClusterRequest{
		Resources: &ClusterResource{CPU: "4"},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := mock.lastRequest().Body
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}
	if _, present := raw["version"]; present {
		t.Errorf("PATCH body must omit version when unchanged, got body: %s", body)
	}
}

func TestDeleteCluster(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("DELETE", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	c := mock.client("acme")
	err := c.DeleteCluster(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeleteCluster_NotFound(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	// Default handler returns 404

	c := mock.client("acme")
	err := c.DeleteCluster(context.Background(), "nonexistent")
	if !IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}
}
