package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// Alertmanager state. Receivers keyed by name; routes is the ordered
	// child route list; global is a single map. The mock does not run the
	// kupe-api validator — tests that need validation should run against
	// the real API. The ETag is a simple counter shared by every section.
	amReceivers map[string]map[string]any
	amRoutes    []map[string]any
	amGlobal    map[string]any
	amETag      string
}

func newMockKupeAPI() *mockKupeAPI {
	m := &mockKupeAPI{
		clusters:    make(map[string]map[string]any),
		secrets:     make(map[string]map[string]any),
		members:     []map[string]any{},
		apiKeys:     make(map[string]map[string]any),
		plans:       make(map[string]map[string]any),
		amReceivers: make(map[string]map[string]any),
		amRoutes:    []map[string]any{},
		amGlobal:    map[string]any{},
		amETag:      `"1"`,
		rvCounter:   1,
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

func (m *mockKupeAPI) close()      { m.server.Close() }
func (m *mockKupeAPI) url() string { return m.server.URL }

// mutateReceiver edits a stored receiver under the mock's lock, simulating
// an out-of-band change (Console UI, another API client). Bumps the shared
// alertmanager ETag like any real write would.
func (m *mockKupeAPI) mutateReceiver(name string, f func(map[string]any)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.amReceivers[name]; ok {
		f(r)
		m.amETag = `"` + m.nextRV() + `"`
	}
}

// seedReceiver / seedRoutes / seedGlobal pre-populate alertmanager state to
// simulate config authored out-of-band (Console UI, another workspace)
// before a terraform Create runs. Used by the MEDIUM-3 "Create must not
// clobber a pre-existing config" tests. Each bumps the shared ETag like a
// real write would.
func (m *mockKupeAPI) seedReceiver(name string, body map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body["name"] = name
	m.amReceivers[name] = body
	m.amETag = `"` + m.nextRV() + `"`
}

func (m *mockKupeAPI) seedRoutes(routes []map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.amRoutes = routes
	m.amETag = `"` + m.nextRV() + `"`
}

func (m *mockKupeAPI) seedGlobal(g map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.amGlobal = g
	m.amETag = `"` + m.nextRV() + `"`
}

func (m *mockKupeAPI) nextRV() string {
	m.rvCounter++
	return fmt.Sprintf("%d", m.rvCounter)
}

func mustEncodeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

func mustDecodeJSON(r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		panic(err)
	}
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
		mustEncodeJSON(w, map[string]any{"items": items})

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/plans/"):
		name := lastSegment(r.URL.Path)
		if p, ok := m.plans[name]; ok {
			mustEncodeJSON(w, p)
		} else {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
		}

	// Tenant
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme":
		w.Header().Set("ETag", `"`+m.tenant["resourceVersion"].(string)+`"`)
		mustEncodeJSON(w, m.tenant)

	// Clusters
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/clusters":
		items := make([]any, 0)
		for _, c := range m.clusters {
			items = append(items, c)
		}
		mustEncodeJSON(w, map[string]any{"items": items})

	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/clusters":
		var body map[string]any
		mustDecodeJSON(r, &body)
		name := body["name"].(string)
		rv := m.nextRV()
		// Mirror kupe-api ≥ v1.5.2: displayName in the request is ignored and
		// the response echoes the cluster NAME. A provider that maps this back
		// into state would produce "inconsistent result after apply".
		cluster := map[string]any{
			"name": name, "displayName": name,
			"type": body["type"], "version": strOrEmpty(body["version"]),
			"resources":       body["resources"],
			"status":          map[string]any{"phase": "Pending"},
			"resourceVersion": rv, "createdAt": "2024-01-01T00:00:00Z",
		}
		m.clusters[name] = cluster
		w.Header().Set("ETag", `"`+rv+`"`)
		w.WriteHeader(201)
		mustEncodeJSON(w, cluster)

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		if c, ok := m.clusters[name]; ok {
			// Simulate the operator advancing the cluster from Pending
			// to Running on first observation so the provider's
			// Create/Update polling loop converges quickly in tests.
			// Real reconciliation is multi-stage (Pending →
			// Provisioning → Running); collapsing it to a single tick
			// keeps acceptance tests fast without changing the
			// production behaviour we're verifying — that the provider
			// blocks until phase reaches Running.
			if status, ok := c["status"].(map[string]any); ok {
				if status["phase"] == "Pending" {
					status["phase"] = "Running"
				}
			}
			w.Header().Set("ETag", `"`+c["resourceVersion"].(string)+`"`)
			mustEncodeJSON(w, c)
		} else {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
		}

	case r.Method == "PATCH" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		c, ok := m.clusters[name]
		if !ok {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
			return
		}
		var body map[string]any
		mustDecodeJSON(r, &body)
		if v, ok := body["version"]; ok {
			c["version"] = v
		}
		if v, ok := body["resources"]; ok {
			c["resources"] = v
		}
		rv := m.nextRV()
		c["resourceVersion"] = rv
		w.Header().Set("ETag", `"`+rv+`"`)
		mustEncodeJSON(w, c)

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		if _, ok := m.clusters[name]; ok {
			delete(m.clusters, name)
			w.WriteHeader(204)
		} else {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
		}

	// Members
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/members":
		mustEncodeJSON(w, map[string]any{"items": m.members})

	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/members":
		var body map[string]any
		mustDecodeJSON(r, &body)
		m.members = append(m.members, body)
		w.WriteHeader(201)
		mustEncodeJSON(w, body)

	case r.Method == "PATCH" && matchPath(r.URL.Path, "/api/v1/tenants/acme/members/"):
		email := lastSegment(r.URL.Path)
		var body map[string]any
		mustDecodeJSON(r, &body)
		for i, member := range m.members {
			if member["email"] == email {
				m.members[i]["role"] = body["role"]
				mustEncodeJSON(w, m.members[i])
				return
			}
		}
		w.WriteHeader(404)
		mustEncodeJSON(w, map[string]string{"error": "not found"})

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
		mustEncodeJSON(w, map[string]string{"error": "not found"})

	// API Keys
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/apikeys":
		items := make([]any, 0)
		for _, k := range m.apiKeys {
			items = append(items, k)
		}
		mustEncodeJSON(w, map[string]any{"items": items})

	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/apikeys":
		var body map[string]any
		mustDecodeJSON(r, &body)
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
		mustEncodeJSON(w, key)

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/apikeys/"):
		id := lastSegment(r.URL.Path)
		if _, ok := m.apiKeys[id]; ok {
			delete(m.apiKeys, id)
			w.WriteHeader(204)
		} else {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
		}

	// Secrets
	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/secrets":
		var body map[string]any
		mustDecodeJSON(r, &body)
		name := body["name"].(string)
		rv := m.nextRV()
		secret := map[string]any{
			"name": name, "secretPath": body["secretPath"],
			// Mirror kupe-api's transformSecret/sliceField contract: the
			// response ALWAYS contains "sync": [] when no targets are set,
			// never null/absent (kupe-api internal/server/handlers.go).
			"sync":            sliceOrEmpty(body["sync"]),
			"status":          map[string]any{"phase": "Pending"},
			"resourceVersion": rv, "createdAt": "2024-01-01T00:00:00Z",
		}
		m.secrets[name] = secret
		w.Header().Set("ETag", `"`+rv+`"`)
		w.WriteHeader(201)
		mustEncodeJSON(w, secret)

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/tenants/acme/secrets/"):
		name := lastSegment(r.URL.Path)
		if s, ok := m.secrets[name]; ok {
			// Same Pending → ready transition as the cluster handler;
			// see that block for the reasoning. ManagedSecret reaches
			// Active once ExternalSecrets has synced to every target.
			if status, ok := s["status"].(map[string]any); ok {
				if status["phase"] == "Pending" {
					status["phase"] = "Active"
				}
			}
			w.Header().Set("ETag", `"`+s["resourceVersion"].(string)+`"`)
			mustEncodeJSON(w, s)
		} else {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
		}

	case r.Method == "PATCH" && matchPath(r.URL.Path, "/api/v1/tenants/acme/secrets/"):
		name := lastSegment(r.URL.Path)
		s, ok := m.secrets[name]
		if !ok {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
			return
		}
		var body map[string]any
		mustDecodeJSON(r, &body)
		if v, ok := body["sync"]; ok {
			s["sync"] = sliceOrEmpty(v)
		}
		rv := m.nextRV()
		s["resourceVersion"] = rv
		w.Header().Set("ETag", `"`+rv+`"`)
		mustEncodeJSON(w, s)

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/secrets/"):
		name := lastSegment(r.URL.Path)
		if _, ok := m.secrets[name]; ok {
			delete(m.secrets, name)
			w.WriteHeader(204)
		} else {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
		}

	// Alertmanager — receivers. Both PUT echoes and GETs mask
	// credential-bearing values with "<secret>", mirroring kupe-api's
	// KA-1 masking (internal/alertmanager/mask.go); stored state stays
	// unmasked, exactly like the real server.
	case r.Method == "PUT" && matchPath(r.URL.Path, "/api/v1/tenants/acme/alertmanager/receivers/"):
		name := lastSegment(r.URL.Path)
		var body map[string]any
		mustDecodeJSON(r, &body)
		body["name"] = name
		m.amReceivers[name] = body
		m.amETag = `"` + m.nextRV() + `"`
		w.Header().Set("ETag", m.amETag)
		mustEncodeJSON(w, mockMaskSecrets(body))

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/tenants/acme/alertmanager/receivers/"):
		name := lastSegment(r.URL.Path)
		recv, ok := m.amReceivers[name]
		if !ok {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
			return
		}
		w.Header().Set("ETag", m.amETag)
		mustEncodeJSON(w, mockMaskSecrets(recv))

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/alertmanager/receivers/"):
		name := lastSegment(r.URL.Path)
		if _, ok := m.amReceivers[name]; !ok {
			w.WriteHeader(404)
			mustEncodeJSON(w, map[string]string{"error": "not found"})
			return
		}
		delete(m.amReceivers, name)
		m.amETag = `"` + m.nextRV() + `"`
		w.WriteHeader(204)

	// Alertmanager — routes (whole list)
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/alertmanager/routes":
		w.Header().Set("ETag", m.amETag)
		mustEncodeJSON(w, map[string]any{"items": m.amRoutes})

	case r.Method == "PUT" && r.URL.Path == "/api/v1/tenants/acme/alertmanager/routes":
		var body struct {
			Items []map[string]any `json:"items"`
		}
		mustDecodeJSON(r, &body)
		// kupe-api decodes routes into typed omitempty structs and
		// re-marshals, so explicit zero-values (continue:false, empty
		// match/group_by) are dropped from what it stores and echoes. Mirror
		// that here so the MEDIUM-4 regression test exercises the real
		// contract.
		m.amRoutes = mockStripRouteZeros(body.Items)
		m.amETag = `"` + m.nextRV() + `"`
		w.Header().Set("ETag", m.amETag)
		mustEncodeJSON(w, map[string]any{"items": m.amRoutes})

	// Alertmanager — global. Masked in responses like receivers above.
	case r.Method == "GET" && r.URL.Path == "/api/v1/tenants/acme/alertmanager/global":
		w.Header().Set("ETag", m.amETag)
		mustEncodeJSON(w, mockMaskSecrets(m.amGlobal))

	case r.Method == "PUT" && r.URL.Path == "/api/v1/tenants/acme/alertmanager/global":
		var body map[string]any
		mustDecodeJSON(r, &body)
		m.amGlobal = body
		m.amETag = `"` + m.nextRV() + `"`
		w.Header().Set("ETag", m.amETag)
		mustEncodeJSON(w, mockMaskSecrets(m.amGlobal))

	default:
		w.WriteHeader(404)
		mustEncodeJSON(w, map[string]string{"error": "not found"})
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

// sliceOrEmpty mirrors kupe-api's sliceField helper: a missing or null
// slice field is always rendered as an empty (non-nil) array in responses.
func sliceOrEmpty(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

// mockSecretKeyFragments mirrors kupe-api's alertmanager mask key matching
// closely enough for tests: url / *_url keys plus well-known credential
// fragments (internal/alertmanager/mask.go isSecretKey).
var mockSecretKeyFragments = []string{
	"password", "secret", "token", "credential",
	"api_key", "apikey", "routing_key", "service_key", "auth_key", "_key",
}

func mockIsSecretKey(key string) bool {
	lower := strings.ToLower(key)
	if lower == "url" || strings.HasSuffix(lower, "_url") {
		return true
	}
	for _, frag := range mockSecretKeyFragments {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

// mockMaskSecrets returns a deep-masked copy of an alertmanager body,
// replacing every credential-bearing value with the "<secret>" sentinel,
// mirroring kupe-api's KA-1 read/write-echo masking. The input is never
// mutated — the mock's stored state stays unmasked like the real server's.
func mockMaskSecrets(body map[string]any) map[string]any {
	if body == nil {
		return nil
	}
	masked, _ := mockMaskValue(body).(map[string]any)
	return masked
}

func mockMaskValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			if k != "name" && mockIsSecretKey(k) && child != nil {
				out[k] = "<secret>"
				continue
			}
			out[k] = mockMaskValue(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = mockMaskValue(child)
		}
		return out
	default:
		return v
	}
}

// mockStripRouteZeros mirrors kupe-api's omitempty routes echo: each stored
// route has its JSON zero-values (false, "", null, [], {}) dropped. Elements
// are kept positionally so ordering is preserved.
func mockStripRouteZeros(items []map[string]any) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, it := range items {
		m, _ := mockStripZeroValue(it).(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		out[i] = m
	}
	return out
}

func mockStripZeroValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, child := range t {
			cs := mockStripZeroValue(child)
			if mockIsZeroValue(cs) {
				continue
			}
			out[k] = cs
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = mockStripZeroValue(child)
		}
		return out
	default:
		return v
	}
}

func mockIsZeroValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// testAccProtoV6ProviderFactories returns provider factories for acceptance tests.
func testAccProtoV6ProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"kupe": providerserver.NewProtocol6WithError(New("test")()),
	}
}
