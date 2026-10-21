package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
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
	// clusterStatuses, clusterStatusesOnPatch, clusterBeforePatch and
	// clusterDeleteConflicts do for clusters what the database fields of
	// the same names do for databases; clusterGets counts cluster GETs.
	clusterStatuses        []map[string]any
	clusterStatusesOnPatch []map[string]any
	clusterBeforePatch     []func(c map[string]any)
	clusterDeleteConflicts int
	clusterGets            int
	// Managed databases keyed by name; bindings keyed by "{database}/{name}".
	databases        map[string]map[string]any
	databaseBindings map[string]map[string]any
	// databasesDisabled makes the database routes answer like a platform
	// with managed databases turned off: reads, deletes, a database PATCH
	// that only changes deletionProtection and a binding PATCH that only
	// removes namespaces work, everything else is refused.
	databasesDisabled bool
	// creationPaused refuses cluster and database creation like kupe-api
	// does while creation is paused.
	creationPaused bool
	// backupsUnavailable answers like a platform that does not offer
	// backups: a database create or PATCH that sets backup is refused, and a
	// database created meanwhile has no backup settings.
	backupsUnavailable bool
	// databaseRequests records each database POST and PATCH body the routes
	// decoded.
	databaseRequests []mockRequest
	// databaseDeleteConflicts is how many upcoming database DELETEs are
	// refused with a plain 409, like a concurrent change does.
	databaseDeleteConflicts int
	// databaseStatuses, when set, are the statuses the next database GETs
	// report in turn, the last one repeating; each is laid over a converged
	// status (Running, observedGeneration caught up, Ready True).
	// databaseGets counts database GETs.
	databaseStatuses []map[string]any
	databaseGets     int
	// databaseRefusals, keyed by method (POST, PATCH), refuse the next
	// database request of that method like kupe-api does, then clear.
	databaseRefusals map[string]mockRefusal
	// databaseStatusesOnPatch, when set, become databaseStatuses once a
	// PATCH succeeds, so the statuses play out after the update.
	databaseStatusesOnPatch []map[string]any
	// databaseBeforePatch is applied, one per PATCH, to the database just
	// before the next PATCHes check If-Match: a write landing between the
	// provider's read and its update.
	databaseBeforePatch []func(db map[string]any)
	// databaseBindingStatus, when set, is the status every binding GET
	// reports instead of the one built from its namespaces, like the
	// platform does for a binding that waits. databaseBindingGets counts
	// binding GETs.
	databaseBindingStatus map[string]any
	databaseBindingGets   int
	// databaseBindingNamespaceStatus, keyed by namespace, is laid over that
	// namespace's Ready status entry, e.g. a Failed phase with a conflict
	// message; the binding's phase, message and Conflict condition follow
	// from its entries the way the platform summarises them.
	databaseBindingNamespaceStatus map[string]map[string]any
	// databaseBindingLagOnPatch is how many binding GETs after a PATCH still
	// report the status of the namespaces delivered before it, like the
	// platform does until it reconciles the new list.
	databaseBindingLagOnPatch int
	// databaseBindingBeforePatch is applied, one per PATCH, to the binding
	// just before the PATCH checks If-Match: a write landing between the
	// provider's read and its update.
	databaseBindingBeforePatch []func(b map[string]any)
	// databaseBindingRequests records each binding POST and PATCH body.
	databaseBindingRequests []mockRequest
	// The registry, its cluster connections and access tokens
	// (testutil_registry_test.go).
	mockRegistryState
}

// mockRequest is a request the mock received: its method, If-Match and body.
type mockRequest struct {
	method, ifMatch string
	body            map[string]any
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

		mockRegistryState: newMockRegistryState(),
		databases:         make(map[string]map[string]any),
		databaseBindings:  make(map[string]map[string]any),
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

	case r.Method == "POST" && r.URL.Path == "/api/v1/tenants/acme/clusters" && m.creationPaused:
		mockError(w, http.StatusBadRequest, client.CodeCreationPaused,
			"Cluster creation is temporarily paused while we expand platform capacity. Please try again later.")

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
			"resources":        body["resources"],
			"highAvailability": body["highAvailability"] == true,
			"generation":       1,
			"status":           map[string]any{"phase": "Pending", "observedGeneration": 0},
			"resourceVersion":  rv, "createdAt": "2024-01-01T00:00:00Z",
		}
		m.clusters[name] = cluster
		w.Header().Set("ETag", `"`+rv+`"`)
		w.WriteHeader(201)
		mustEncodeJSON(w, cluster)

	case r.Method == "GET" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		if c, ok := m.clusters[name]; ok {
			// Simulate the operator bringing the cluster to Running with
			// its latest spec applied on first observation, so the
			// provider's Create/Update polling loop converges quickly in
			// tests. Real reconciliation is multi-stage (Pending →
			// Provisioning → Running); clusterStatuses scripts the stages
			// a test needs.
			m.clusterGets++
			status := map[string]any{"phase": "Running", "observedGeneration": c["generation"]}
			if len(m.clusterStatuses) > 0 {
				for k, v := range m.clusterStatuses[0] {
					status[k] = v
				}
				if len(m.clusterStatuses) > 1 {
					m.clusterStatuses = m.clusterStatuses[1:]
				}
			}
			c["status"] = status
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
		if len(m.clusterBeforePatch) > 0 {
			m.clusterBeforePatch[0](c)
			m.clusterBeforePatch = m.clusterBeforePatch[1:]
			c["resourceVersion"] = m.nextRV()
		}
		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && strings.Trim(ifMatch, `"`) != c["resourceVersion"] {
			mockError(w, http.StatusPreconditionFailed, "", "resource has been modified (ETag mismatch)")
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
		c["generation"] = c["generation"].(int) + 1
		rv := m.nextRV()
		c["resourceVersion"] = rv
		if m.clusterStatusesOnPatch != nil {
			m.clusterStatuses = m.clusterStatusesOnPatch
		}
		w.Header().Set("ETag", `"`+rv+`"`)
		mustEncodeJSON(w, c)

	case r.Method == "DELETE" && matchPath(r.URL.Path, "/api/v1/tenants/acme/clusters/"):
		name := lastSegment(r.URL.Path)
		if _, ok := m.clusters[name]; ok {
			if m.clusterDeleteConflicts > 0 {
				m.clusterDeleteConflicts--
				mockError(w, http.StatusConflict, "", "the cluster changed while it was being deleted, retry")
				return
			}
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

	case strings.HasPrefix(r.URL.Path, mockDatabasesPath):
		m.serveDatabases(w, r)

	case r.URL.Path == mockRegistriesPath || strings.HasPrefix(r.URL.Path, mockRegistriesPath+"/"):
		m.serveRegistries(w, r)

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

// --- Managed databases ---

const mockDatabasesPath = "/api/v1/tenants/acme/databases"

// mockDatabasePatchFields are the only fields kupe-api's PATCH accepts.
var mockDatabasePatchFields = map[string]bool{
	"version": true, "resources": true, "highAvailability": true, "deletionProtection": true, "backup": true,
}

func mockError(w http.ResponseWriter, status int, code, msg string) {
	mockFieldError(w, status, code, "", msg)
}

// mockFieldError is mockError naming the spec field the error applies to;
// kupe-api sends a field only with a code.
func mockFieldError(w http.ResponseWriter, status int, code, field, msg string) {
	w.WriteHeader(status)
	body := map[string]string{"error": msg}
	if code != "" {
		body["code"], body["severity"], body["message"] = code, "error", msg
		if field != "" {
			body["field"] = field
		}
	}
	mustEncodeJSON(w, body)
}

// Backup settings the mock platform offers, as the operator chart publishes
// them.
const (
	mockBackupRetentionMin     = 7
	mockBackupRetentionMax     = 35
	mockBackupRetentionDefault = 7
)

// mockBackup checks a backup object a database create or PATCH sent the way
// kupe-api does, and returns stored with the sent keys laid over it and the
// CRD's schedule default filled in. It answers a refusal itself and returns
// false. A retention is checked only when it changes, like kupe-api.
func (m *mockKupeAPI) mockBackup(w http.ResponseWriter, sent any, stored map[string]any, create bool) (map[string]any, bool) {
	if m.backupsUnavailable {
		mockFieldError(w, http.StatusBadRequest, client.CodeBackupsUnavailable, "spec.backup",
			"backups are not available on this platform")
		return nil, false
	}
	backup, ok := sent.(map[string]any)
	if !ok {
		mockError(w, http.StatusBadRequest, "", "invalid request body: backup must be an object")
		return nil, false
	}
	for key := range backup {
		if key != "retention" && key != "schedule" {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return nil, false
		}
	}
	if !create && len(backup) == 0 {
		mockError(w, http.StatusBadRequest, "", "backup must set at least one of retention, schedule")
		return nil, false
	}
	if schedule, set := backup["schedule"]; set && schedule != "Daily" && schedule != "DailyThenWeekly" {
		mockError(w, http.StatusBadRequest, "", "backup.schedule must be one of Daily, DailyThenWeekly")
		return nil, false
	}
	if retention, set := backup["retention"]; set && retention != stored["retention"] {
		if days, _ := retention.(float64); days < mockBackupRetentionMin || days > mockBackupRetentionMax {
			mockFieldError(w, http.StatusBadRequest, "DATABASE_BACKUP_RETENTION_OUT_OF_RANGE", "spec.backup.retention",
				fmt.Sprintf("backup.retention must be between %d and %d days", mockBackupRetentionMin, mockBackupRetentionMax))
			return nil, false
		}
	}
	merged := map[string]any{}
	for k, v := range stored {
		merged[k] = v
	}
	for k, v := range backup {
		merged[k] = v
	}
	if _, set := merged["schedule"]; !set {
		merged["schedule"] = "Daily"
	}
	return merged, true
}

// serveDatabases routes /databases[/{name}[/bindings[/{binding}]]]. Like the
// other mock routes it advances status on GET: a database goes Pending ->
// Running and catches observedGeneration up with generation; a binding goes
// Pending -> Ready. Called with m.mu held.
func (m *mockKupeAPI) serveDatabases(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, mockDatabasesPath), "/")
	var segs []string
	if rest != "" {
		segs = strings.Split(rest, "/")
	}
	if m.databasesDisabled && !m.allowedWhileDisabled(r, segs) {
		mockError(w, http.StatusNotFound, client.CodeDatabasesUnavailable, "managed databases are not enabled")
		return
	}
	if refusal, ok := m.databaseRefusals[r.Method]; ok && len(segs) <= 1 {
		delete(m.databaseRefusals, r.Method)
		mockError(w, refusal.status, refusal.code, refusal.message)
		return
	}
	switch {
	case len(segs) == 0 && r.Method == http.MethodPost:
		m.createDatabase(w, r)
	case len(segs) == 1 && r.Method == http.MethodGet:
		m.getDatabase(w, segs[0])
	case len(segs) == 1 && r.Method == http.MethodPatch:
		m.patchDatabase(w, r, segs[0])
	case len(segs) == 1 && r.Method == http.MethodDelete:
		m.deleteDatabase(w, segs[0])
	case len(segs) == 2 && segs[1] == "bindings" && r.Method == http.MethodPost:
		m.createDatabaseBinding(w, r, segs[0])
	case len(segs) == 3 && segs[1] == "bindings" && r.Method == http.MethodGet:
		m.getDatabaseBinding(w, segs[0], segs[2])
	case len(segs) == 3 && segs[1] == "bindings" && r.Method == http.MethodPatch:
		m.patchDatabaseBinding(w, r, segs[0], segs[2])
	case len(segs) == 3 && segs[1] == "bindings" && r.Method == http.MethodDelete:
		m.deleteDatabaseBinding(w, segs[0], segs[2])
	default:
		mockError(w, http.StatusNotFound, "", "not found")
	}
}

// allowedWhileDisabled reports whether kupe-api still serves r with
// databases disabled: reads, deletes, a database PATCH of
// deletionProtection alone, and a binding PATCH that lists no namespace the
// stored binding does not, so existing databases and bindings can be
// removed. It restores the body it reads. Called with m.mu held.
func (m *mockKupeAPI) allowedWhileDisabled(r *http.Request, segs []string) bool {
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		return true
	case http.MethodPatch:
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return false
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			return false
		}
		if len(segs) == 3 && segs[1] == "bindings" {
			return m.onlyRemovesNamespaces(segs[0]+"/"+segs[2], body)
		}
		_, ok := body["deletionProtection"]
		return ok && len(body) == 1
	default:
		return false
	}
}

// onlyRemovesNamespaces reports whether a binding PATCH body lists only
// namespaces the stored binding key already lists.
func (m *mockKupeAPI) onlyRemovesNamespaces(key string, body map[string]any) bool {
	binding, ok := m.databaseBindings[key]
	if !ok {
		return true // answered 404 not found by the route
	}
	requested, ok := mockNamespaces(body["namespaces"])
	if !ok {
		return true // answered 400 by the route
	}
	stored, _ := binding["namespaces"].([]string)
	for _, namespace := range requested {
		if !slices.Contains(stored, namespace) {
			return false
		}
	}
	return true
}

func (m *mockKupeAPI) createDatabase(w http.ResponseWriter, r *http.Request) {
	if m.creationPaused {
		mockError(w, http.StatusBadRequest, client.CodeCreationPaused,
			"Database creation is temporarily paused while we expand platform capacity. Please try again later.")
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	m.databaseRequests = append(m.databaseRequests, mockRequest{method: r.Method, body: body})
	name := strOrEmpty(body["name"])
	if _, exists := m.databases[name]; exists {
		mockError(w, http.StatusConflict, "", "database already exists")
		return
	}
	engine := strOrEmpty(body["engine"])
	if engine == "" {
		engine = "postgres"
	}
	if engine != "postgres" {
		mockError(w, http.StatusBadRequest, "", fmt.Sprintf("unsupported engine %q", engine))
		return
	}
	version := strOrEmpty(body["version"])
	if version == "" {
		version = "18"
	}
	protected, set := body["deletionProtection"].(bool)
	// The operator's defaulter gives a new database the default retention
	// while backups are offered (CREATE only); the CRD defaults the schedule.
	var backup map[string]any
	if sent, ok := body["backup"]; ok {
		if backup, ok = m.mockBackup(w, sent, nil, true); !ok {
			return
		}
	} else if !m.backupsUnavailable {
		backup = map[string]any{"schedule": "Daily"}
	}
	if _, ok := backup["retention"]; backup != nil && !ok {
		backup["retention"] = float64(mockBackupRetentionDefault)
	}
	rv := m.nextRV()
	db := map[string]any{
		"name": name, "engine": engine, "version": version,
		"resources":          body["resources"],
		"highAvailability":   body["highAvailability"] == true,
		"deletionProtection": protected || !set,
		"backup":             backup,
		"generation":         1,
		"status":             map[string]any{"phase": "Pending", "observedGeneration": 0, "currentVersion": ""},
		"resourceVersion":    rv, "createdAt": "2026-10-09T10:00:00Z",
	}
	m.databases[name] = db
	w.Header().Set("ETag", `"`+rv+`"`)
	w.WriteHeader(http.StatusCreated)
	mustEncodeJSON(w, db)
}

func (m *mockKupeAPI) getDatabase(w http.ResponseWriter, name string) {
	db, ok := m.databases[name]
	if !ok {
		mockError(w, http.StatusNotFound, "", "not found")
		return
	}
	m.databaseGets++
	status := map[string]any{
		"phase":              "Running",
		"observedGeneration": db["generation"],
		"currentVersion":     db["version"].(string) + ".0",
		"conditions":         []any{map[string]any{"type": "Ready", "status": "True", "reason": "Healthy"}},
		"lastBackupAt":       "",
		"recoveryWindow":     nil,
		"backupSizeBytes":    0,
	}
	// A database with backup settings reports the backups taken.
	if backup, _ := db["backup"].(map[string]any); backup != nil {
		status["lastBackupAt"] = "2026-10-21T03:17:00Z"
		status["recoveryWindow"] = map[string]any{"earliest": "2026-10-14T03:17:00Z", "latest": "2026-10-21T09:00:00Z"}
		status["backupSizeBytes"] = int64(5368709120)
	}
	if len(m.databaseStatuses) > 0 {
		for k, v := range m.databaseStatuses[0] {
			status[k] = v
		}
		if len(m.databaseStatuses) > 1 {
			m.databaseStatuses = m.databaseStatuses[1:]
		}
	}
	db["status"] = status
	w.Header().Set("ETag", `"`+db["resourceVersion"].(string)+`"`)
	mustEncodeJSON(w, db)
}

func (m *mockKupeAPI) patchDatabase(w http.ResponseWriter, r *http.Request, name string) {
	db, ok := m.databases[name]
	if !ok {
		mockError(w, http.StatusNotFound, "", "not found")
		return
	}
	if len(m.databaseBeforePatch) > 0 {
		m.databaseBeforePatch[0](db)
		m.databaseBeforePatch = m.databaseBeforePatch[1:]
		db["resourceVersion"] = m.nextRV()
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && strings.Trim(ifMatch, `"`) != db["resourceVersion"] {
		mockError(w, http.StatusPreconditionFailed, "", "resource has been modified (ETag mismatch)")
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	m.databaseRequests = append(m.databaseRequests, mockRequest{method: r.Method, ifMatch: r.Header.Get("If-Match"), body: body})
	if len(body) == 0 {
		mockError(w, http.StatusBadRequest, "", "at least one field required")
		return
	}
	for key := range body {
		if !mockDatabasePatchFields[key] {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("unknown field %q", key))
			return
		}
	}
	if v, ok := body["version"]; ok && v != db["version"] {
		mockError(w, http.StatusBadRequest, client.CodeMajorUpgradeUnavailable, "major version upgrades are not available yet")
		return
	}
	var backup map[string]any
	if sent, ok := body["backup"]; ok {
		stored, _ := db["backup"].(map[string]any)
		if backup, ok = m.mockBackup(w, sent, stored, false); !ok {
			return
		}
	}
	if res, ok := body["resources"].(map[string]any); ok {
		current, _ := db["resources"].(map[string]any)
		if storage, ok := res["storage"].(string); ok {
			requested, okRequested := parseSizeQuantity(storage)
			stored, okStored := parseSizeQuantity(strOrEmpty(current["storage"]))
			if okRequested && okStored && requested.Cmp(stored) < 0 {
				mockError(w, http.StatusBadRequest, client.CodeStorageShrinkUnsupported,
					fmt.Sprintf("storage can only grow: %s is smaller than the current %s", storage, current["storage"]))
				return
			}
		}
		for k, v := range res {
			current[k] = v
		}
	}
	for _, key := range []string{"highAvailability", "deletionProtection"} {
		if v, ok := body[key]; ok {
			db[key] = v
		}
	}
	if backup != nil {
		db["backup"] = backup
	}
	db["generation"] = db["generation"].(int) + 1
	rv := m.nextRV()
	db["resourceVersion"] = rv
	if m.databaseStatusesOnPatch != nil {
		m.databaseStatuses = m.databaseStatusesOnPatch
	}
	w.Header().Set("ETag", `"`+rv+`"`)
	mustEncodeJSON(w, db)
}

func (m *mockKupeAPI) deleteDatabase(w http.ResponseWriter, name string) {
	db, ok := m.databases[name]
	if !ok {
		mockError(w, http.StatusNotFound, "", "not found")
		return
	}
	if m.databaseDeleteConflicts > 0 {
		m.databaseDeleteConflicts--
		mockError(w, http.StatusConflict, "", "the database changed while it was being deleted, retry")
		return
	}
	if db["deletionProtection"] == true {
		mockError(w, http.StatusConflict, client.CodeDeletionProtected,
			fmt.Sprintf("database %q has deletionProtection enabled: set it to false before deleting", name))
		return
	}
	delete(m.databases, name)
	for key := range m.databaseBindings {
		if strings.HasPrefix(key, name+"/") {
			delete(m.databaseBindings, key)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// mockBindingCreateFields are the fields kupe-api accepts when a binding is
// created; it refuses any other field, the old `namespace` included.
var mockBindingCreateFields = map[string]bool{"name": true, "cluster": true, "namespaces": true, "role": true, "secretName": true}

func (m *mockKupeAPI) createDatabaseBinding(w http.ResponseWriter, r *http.Request, database string) {
	if _, ok := m.databases[database]; !ok {
		mockError(w, http.StatusNotFound, "", "database not found")
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	m.databaseBindingRequests = append(m.databaseBindingRequests, mockRequest{method: r.Method, body: body})
	for key := range body {
		if !mockBindingCreateFields[key] {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	name := strOrEmpty(body["name"])
	if name == "" {
		mockError(w, http.StatusBadRequest, "", `invalid name ""`)
		return
	}
	namespaces, ok := mockNamespaces(body["namespaces"])
	if !ok {
		mockError(w, http.StatusBadRequest, "", "namespaces is required: list at least one namespace")
		return
	}
	key := database + "/" + name
	if _, exists := m.databaseBindings[key]; exists {
		mockError(w, http.StatusConflict, "", "binding already exists")
		return
	}
	rv := m.nextRV()
	binding := map[string]any{
		"name": name, "database": database,
		"cluster": body["cluster"], "namespaces": namespaces, "role": body["role"],
		"secretName": strOrEmpty(body["secretName"]),
		"status": map[string]any{
			"phase": "Pending", "secretName": "", "message": "", "namespaces": []any{}, "conditions": []any{},
		},
		// delivered (and lag, set by a PATCH) are the mock's own record
		// of the namespaces the platform has delivered into; the API
		// returns neither.
		"delivered":       namespaces,
		"resourceVersion": rv, "createdAt": "2026-10-09T10:05:00Z",
	}
	m.databaseBindings[key] = binding
	w.Header().Set("ETag", `"`+rv+`"`)
	w.WriteHeader(http.StatusCreated)
	mustEncodeJSON(w, mockBindingView(binding))
}

// mockNamespaces is a binding's namespace list from a request body, false
// when it is missing or empty.
func mockNamespaces(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}
	namespaces := make([]string, 0, len(list))
	for _, ns := range list {
		namespaces = append(namespaces, strOrEmpty(ns))
	}
	return namespaces, true
}

// mockBindingView is the binding as kupe-api returns it.
func mockBindingView(binding map[string]any) map[string]any {
	view := make(map[string]any, len(binding))
	for k, v := range binding {
		if k != "delivered" && k != "lag" {
			view[k] = v
		}
	}
	return view
}

func (m *mockKupeAPI) getDatabaseBinding(w http.ResponseWriter, database, name string) {
	binding, ok := m.databaseBindings[database+"/"+name]
	if !ok {
		mockError(w, http.StatusNotFound, "", "binding not found for this database")
		return
	}
	m.databaseBindingGets++
	secretName := binding["secretName"].(string)
	if secretName == "" {
		secretName = database + "-" + binding["role"].(string)
	}
	reported := binding["namespaces"].([]string)
	if lag, _ := binding["lag"].(int); lag > 0 {
		binding["lag"] = lag - 1
		reported = binding["delivered"].([]string)
	} else {
		binding["delivered"] = reported
	}
	status := m.mockBindingStatus(database, reported)
	status["secretName"] = secretName
	if m.databaseBindingStatus != nil {
		status = map[string]any{"secretName": secretName, "namespaces": []any{}}
		for k, v := range m.databaseBindingStatus {
			status[k] = v
		}
	}
	binding["status"] = status
	w.Header().Set("ETag", `"`+binding["resourceVersion"].(string)+`"`)
	mustEncodeJSON(w, mockBindingView(binding))
}

// mockBindingStatus is the status of a binding that delivered into
// namespaces: an entry per namespace, Ready unless
// databaseBindingNamespaceStatus says otherwise, and the binding Failed
// while any is Failed, Ready when all are Ready, else Pending.
func (m *mockKupeAPI) mockBindingStatus(database string, namespaces []string) map[string]any {
	entries := make([]any, 0, len(namespaces))
	phase := "Ready"
	var conflicts []string
	for _, ns := range namespaces {
		entry := map[string]any{
			"name": ns, "host": fmt.Sprintf("%s.%s.svc.cluster.local", database, ns),
			"phase": "Ready", "deliveredRotation": "", "message": "",
		}
		for k, v := range m.databaseBindingNamespaceStatus[ns] {
			entry[k] = v
		}
		switch entry["phase"] {
		case "Failed":
			phase = "Failed"
			conflicts = append(conflicts, strOrEmpty(entry["message"]))
		case "Ready":
		default:
			if phase != "Failed" {
				phase = "Pending"
			}
		}
		entries = append(entries, entry)
	}
	conflict := map[string]any{"type": "Conflict", "status": "False", "reason": "NoConflict"}
	message := ""
	if len(conflicts) > 0 {
		message = strings.Join(conflicts, "; ")
		conflict = map[string]any{"type": "Conflict", "status": "True", "reason": "SyncConflict", "message": message}
	}
	return map[string]any{"phase": phase, "message": message, "namespaces": entries, "conditions": []any{conflict}}
}

// patchDatabaseBinding answers like kupe-api: the body is only the new
// namespace list, checked against If-Match. The status keeps describing the
// namespaces delivered before for databaseBindingLagOnPatch GETs.
func (m *mockKupeAPI) patchDatabaseBinding(w http.ResponseWriter, r *http.Request, database, name string) {
	binding, ok := m.databaseBindings[database+"/"+name]
	if !ok {
		mockError(w, http.StatusNotFound, "", "binding not found for this database")
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	m.databaseBindingRequests = append(m.databaseBindingRequests,
		mockRequest{method: r.Method, ifMatch: r.Header.Get("If-Match"), body: body})
	if len(m.databaseBindingBeforePatch) > 0 {
		m.databaseBindingBeforePatch[0](binding)
		m.databaseBindingBeforePatch = m.databaseBindingBeforePatch[1:]
		binding["resourceVersion"] = m.nextRV()
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && strings.Trim(ifMatch, `"`) != binding["resourceVersion"] {
		mockError(w, http.StatusPreconditionFailed, "", "resource has been modified (ETag mismatch)")
		return
	}
	for key := range body {
		if key != "namespaces" {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	namespaces, ok := mockNamespaces(body["namespaces"])
	if !ok {
		mockError(w, http.StatusBadRequest, "", "namespaces is required: list at least one namespace")
		return
	}
	binding["namespaces"] = namespaces
	binding["lag"] = m.databaseBindingLagOnPatch
	rv := m.nextRV()
	binding["resourceVersion"] = rv
	w.Header().Set("ETag", `"`+rv+`"`)
	mustEncodeJSON(w, mockBindingView(binding))
}

// mockBindingPatches are the bodies of the binding PATCHes the mock
// received, in order.
func (m *mockKupeAPI) mockBindingPatches() []mockRequest {
	var patches []mockRequest
	for _, req := range m.databaseBindingRequests {
		if req.method == http.MethodPatch {
			patches = append(patches, req)
		}
	}
	return patches
}

func (m *mockKupeAPI) deleteDatabaseBinding(w http.ResponseWriter, database, name string) {
	key := database + "/" + name
	if _, ok := m.databaseBindings[key]; !ok {
		mockError(w, http.StatusNotFound, "", "binding not found for this database")
		return
	}
	delete(m.databaseBindings, key)
	w.WriteHeader(http.StatusNoContent)
}

// mockRefusal is an error envelope the mock answers with.
type mockRefusal struct {
	status        int
	code, message string
}

// mockDatabaseStatus is a database status for databaseStatuses: phase, and
// the Ready condition with message.
func mockDatabaseStatus(phase string, ready bool, message string) map[string]any {
	status := "False"
	if ready {
		status = "True"
	}
	return map[string]any{
		"phase":      phase,
		"conditions": []any{map[string]any{"type": "Ready", "status": status, "reason": "NotHealthy", "message": message}},
	}
}

// setState changes the mock's state under its lock, for tests that
// simulate the platform changing between steps.
func (m *mockKupeAPI) setState(f func(m *mockKupeAPI)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(m)
}
