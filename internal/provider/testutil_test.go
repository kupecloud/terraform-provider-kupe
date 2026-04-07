package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// mockKupeAPI is a stateful mock API server for acceptance tests.
// It stores resources in memory and supports CRUD operations.
type mockKupeAPI struct {
	server    *httptest.Server
	mu        sync.Mutex
	clusters  map[string]map[string]any
	secrets   map[string]map[string]any
	members   []map[string]any
	apiKeys   map[string]map[string]any
	tenant    map[string]any
	plans     map[string]map[string]any
	rvCounter int
}

func newMockKupeAPI() *mockKupeAPI {
	m := &mockKupeAPI{
		clusters:  make(map[string]map[string]any),
		secrets:   make(map[string]map[string]any),
		members:   []map[string]any{},
		apiKeys:   make(map[string]map[string]any),
		plans:     make(map[string]map[string]any),
		rvCounter: 1,
		tenant: map[string]any{
			"name": "acme", "displayName": "Acme Corp",
			"contactEmail": "admin@acme.com", "plan": "starter",
			"enforceMetricsLimit": true, "enforceLogLimit": true,
			"resourceVersion": "1", "createdAt": "2024-01-01T00:00:00Z",
		},
	}

	m.plans["starter"] = map[string]any{
		"name": "starter", "displayName": "Starter",
		"platformFee": "29.00", "maxClusters": float64(3),
	}

	m.server = httptest.NewServer(http.HandlerFunc(m.handler))
	return m
}

func (m *mockKupeAPI) close() { m.server.Close() }
func (m *mockKupeAPI) url() string { return m.server.URL }

func (m *mockKupeAPI) nextRV() string {
	m.rvCounter++
	return fmt.Sprintf("%d", m.rvCounter)
}

func (m *mockKupeAPI) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	// Route matching
	switch {
	// Plans
	case r.Method == "GET" && r.URL.Path == "/api/v1/plans":
		items := make([]any, 0)
		for _, p := range m.plans {
			items = append(items, p)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/plans/"):
		name := lastSegment(r.URL.Path)
		if p, ok := m.plans[name]; ok {
			json.NewEncoder(w).Encode(p)
		} else {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}

	// Tenant
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme":
		w.Header().Set("ETag", `"`+m.tenant["resourceVersion"].(string)+`"`)
		json.NewEncoder(w).Encode(m.tenant)

	// Clusters
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/clusters":
		items := make([]any, 0)
		for _, c := range m.clusters {
			items = append(items, c)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})

	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/clusters":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		name := body["name"].(string)
		rv := m.nextRV()
		cluster := map[string]any{
			"name": name, "displayName": body["displayName"],
			"type": body["type"], "version": strOrEmpty(body["version"]),
			"resources": body["resources"],
			"status":    map[string]any{"phase": "Pending"},
			"resourceVersion": rv, "createdAt": "2024-01-01T00:00:00Z",
		}
		m.clusters[name] = cluster
		w.Header().Set("ETag", `"`+rv+`"`)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(cluster)

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		if c, ok := m.clusters[name]; ok {
			w.Header().Set("ETag", `"`+c["resourceVersion"].(string)+`"`)
			json.NewEncoder(w).Encode(c)
		} else {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}

	case r.Method == "PATCH" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		c, ok := m.clusters[name]
		if !ok {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["version"]; ok {
			c["version"] = v
		}
		if v, ok := body["resources"]; ok {
			c["resources"] = v
		}
		rv := m.nextRV()
		c["resourceVersion"] = rv
		w.Header().Set("ETag", `"`+rv+`"`)
		json.NewEncoder(w).Encode(c)

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		if _, ok := m.clusters[name]; ok {
			delete(m.clusters, name)
			w.WriteHeader(204)
		} else {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}

	// Members
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/members":
		json.NewEncoder(w).Encode(map[string]any{"items": m.members})

	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/members":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.members = append(m.members, body)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(body)

	case r.Method == "PATCH" && matchPath(r.URL.Path, "/api/v1/tenants/acme/members/"):
		email := lastSegment(r.URL.Path)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		for i, member := range m.members {
			if member["email"] == email {
				m.members[i]["role"] = body["role"]
				json.NewEncoder(w).Encode(m.members[i])
				return
			}
		}
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/members/"):
		email := lastSegment(r.URL.Path)
		for i, member := range m.members {
			if member["email"] == email {
				m.members = append(m.members[:i], m.members[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})

	// API Keys
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/apikeys":
		items := make([]any, 0)
		for _, k := range m.apiKeys {
			items = append(items, k)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})

	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/apikeys":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		id := fmt.Sprintf("ak-%d", m.rvCounter)
		key := map[string]any{
			"id": id, "displayName": body["displayName"],
			"role": body["role"], "createdBy": "test@acme.com",
			"key": "kupe_test_" + id, "createdAt": "2024-01-01T00:00:00Z",
		}
		if v, ok := body["expiresAt"]; ok {
			key["expiresAt"] = v
		}
		m.apiKeys[id] = key
		m.rvCounter++
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(key)

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/apikeys/"):
		id := lastSegment(r.URL.Path)
		if _, ok := m.apiKeys[id]; ok {
			delete(m.apiKeys, id)
			w.WriteHeader(204)
		} else {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}

	// Secrets
	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/secrets":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		name := body["name"].(string)
		rv := m.nextRV()
		secret := map[string]any{
			"name": name, "secretPath": body["secretPath"],
			"sync":   body["sync"],
			"status": map[string]any{"phase": "Pending"},
			"resourceVersion": rv, "createdAt": "2024-01-01T00:00:00Z",
		}
		m.secrets[name] = secret
		w.Header().Set("ETag", `"`+rv+`"`)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(secret)

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/tenants/acme/secrets/"):
		name := lastSegment(r.URL.Path)
		if s, ok := m.secrets[name]; ok {
			w.Header().Set("ETag", `"`+s["resourceVersion"].(string)+`"`)
			json.NewEncoder(w).Encode(s)
		} else {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}

	case r.Method == "PATCH" && matchPath(r.URL.Path, "/api/v1/tenants/acme/secrets/"):
		name := lastSegment(r.URL.Path)
		s, ok := m.secrets[name]
		if !ok {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["sync"]; ok {
			s["sync"] = v
		}
		rv := m.nextRV()
		s["resourceVersion"] = rv
		w.Header().Set("ETag", `"`+rv+`"`)
		json.NewEncoder(w).Encode(s)

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/secrets/"):
		name := lastSegment(r.URL.Path)
		if _, ok := m.secrets[name]; ok {
			delete(m.secrets, name)
			w.WriteHeader(204)
		} else {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}

	default:
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

// --- Path helpers ---

func matchPath(path, prefix string) bool {
	return len(path) > len(prefix) && path[:len(prefix)] == prefix
}

func lastSegment(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func strOrEmpty(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// testAccProtoV6ProviderFactories returns provider factories for acceptance tests.
func testAccProtoV6ProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"kupe": providerserver.NewProtocol6WithError(New("test")()),
	}
}
