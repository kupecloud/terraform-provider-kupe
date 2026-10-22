package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// The registry part of mockKupeAPI. It answers like kupe-api at
// kupe-api stream/registry-v1 (4551c9d): one registry per tenant, named
// registry; cluster connections named after their cluster; access tokens
// whose secret is in the create response only. Called with m.mu held.

const mockRegistriesPath = "/api/v1/tenants/acme/registries"

// mockRegistryState is the registry half of the mock's state.
type mockRegistryState struct {
	// registries is keyed by name ("registry"); registryBindings by
	// cluster; registryTokens by id.
	registries       map[string]map[string]any
	registryBindings map[string]map[string]any
	registryTokens   map[string]map[string]any
	// registriesDisabled answers like a platform with the registry switched
	// off: reads, deletes, a registry PATCH of deletionProtection alone and a
	// connection PATCH that only removes push namespaces are served, anything
	// else is 404 registries_unavailable.
	registriesDisabled bool
	// registryReadsUnavailable answers every registry GET with 404
	// registries_unavailable, as a kupe-api without a registry service would.
	registryReadsUnavailable bool
	// registryRoutesMissing answers every registry route like a kupe-api
	// older than registries: the HTTP router's plain-text 404.
	registryRoutesMissing bool
	// registryStatuses, when set, are laid over the converged status of the
	// next registry GETs in turn, the last one repeating.
	registryStatuses []map[string]any
	// registryDeleteLag is how many GETs after an accepted DELETE still find
	// the registry, Terminating.
	registryDeleteLag int
	// registryBindingStatus, when set, is laid over every connection's
	// built status.
	registryBindingStatus map[string]any
	// registryBindingLagOnPatch is how many connection GETs after a PATCH
	// still report the push namespaces delivered before it (phase Ready),
	// like the platform does until it reconciles the new list.
	registryBindingLagOnPatch int
	// registryTokenPhases, when set, are the phases the next token GETs
	// report in turn, the last one repeating (default Ready).
	registryTokenPhases []string
	// registryTokenIssuedUsername, when set, is the username a token reports
	// once issued, in place of the predicted one.
	registryTokenIssuedUsername string
	// registryTokenLimit is the most access tokens the registry holds.
	registryTokenLimit int
	// registryRequests records every registry request in order.
	registryRequests []mockRegistryRequest
}

// mockRegistryRequest is a registry request the mock received.
type mockRegistryRequest struct {
	method, path, ifMatch string
	body                  map[string]any
}

func newMockRegistryState() mockRegistryState {
	return mockRegistryState{
		registries:         make(map[string]map[string]any),
		registryBindings:   make(map[string]map[string]any),
		registryTokens:     make(map[string]map[string]any),
		registryTokenLimit: 50,
	}
}

// mockRegistryStorageLimit is the CRD's spec.storageLimit pattern.
var mockRegistryStorageLimit = regexp.MustCompile(`^\d+(\.\d+)?(Mi|Gi|Ti)$`)

// mockPlanRegistryCapBytes is the starter plan's registry cap, 5Gi.
const mockPlanRegistryCapBytes = int64(5) << 30

// mockNotFound answers 404 with kupe-api's wording for a named object.
func mockNotFound(w http.ResponseWriter, noun, name string) {
	mockError(w, http.StatusNotFound, "", fmt.Sprintf("%s %q not found", noun, name))
}

func mockRegistriesUnavailable(w http.ResponseWriter) {
	mockError(w, http.StatusNotFound, client.CodeRegistriesUnavailable, "registries are not available on this platform")
}

// serveRegistries routes /registries[/{name}[/bindings|tokens[/{id}]]].
func (m *mockKupeAPI) serveRegistries(w http.ResponseWriter, r *http.Request) {
	if m.registryRoutesMissing {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "404 page not found\n")
		return
	}
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var logged map[string]any
	_ = json.Unmarshal(raw, &logged)
	m.registryRequests = append(m.registryRequests, mockRegistryRequest{
		method: r.Method, path: r.URL.Path, ifMatch: r.Header.Get("If-Match"), body: logged,
	})

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, mockRegistriesPath), "/")
	var segs []string
	if rest != "" {
		segs = strings.Split(rest, "/")
	}
	if r.Method == http.MethodGet && m.registryReadsUnavailable {
		mockRegistriesUnavailable(w)
		return
	}
	// Any {name} other than the registry's one name is not found.
	if len(segs) > 0 && segs[0] != "registry" {
		mockNotFound(w, "registry", segs[0])
		return
	}
	switch {
	case len(segs) == 0 && r.Method == http.MethodPost:
		m.createRegistry(w, r)
	case len(segs) == 1 && r.Method == http.MethodGet:
		m.getRegistry(w, segs[0])
	case len(segs) == 1 && r.Method == http.MethodPatch:
		m.patchRegistry(w, r, segs[0])
	case len(segs) == 1 && r.Method == http.MethodDelete:
		m.deleteRegistry(w, segs[0])
	case len(segs) >= 2 && segs[1] == "bindings":
		m.serveRegistryBindings(w, r, segs)
	case len(segs) >= 2 && segs[1] == "tokens":
		m.serveRegistryTokens(w, r, segs)
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "404 page not found\n")
	}
}

// mockRegistryRequestsOf are the requests of method to path, in order.
func (m *mockKupeAPI) mockRegistryRequestsOf(method, path string) []mockRegistryRequest {
	var out []mockRegistryRequest
	for _, req := range m.registryRequests {
		if req.method == method && req.path == path {
			out = append(out, req)
		}
	}
	return out
}

func (m *mockKupeAPI) createRegistry(w http.ResponseWriter, r *http.Request) {
	if m.registriesDisabled {
		mockRegistriesUnavailable(w)
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	name := strOrEmpty(body["name"])
	if name == "" {
		name = "registry"
	}
	if name != "registry" {
		mockError(w, http.StatusBadRequest, "", "a tenant has one registry, named registry")
		return
	}
	limit := strOrEmpty(body["storageLimit"])
	if limit != "" && !mockRegistryStorageLimit.MatchString(limit) {
		mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid storageLimit %q", limit))
		return
	}
	if len(m.registries) > 0 {
		mockError(w, http.StatusConflict, client.CodeRegistryAlreadyExists, "this tenant already has a registry")
		return
	}
	protected, set := body["deletionProtection"].(bool)
	rv := m.nextRV()
	reg := map[string]any{
		"name": name, "storageLimit": limit, "deletionProtection": protected || !set,
		"generation": 1,
		"status": map[string]any{
			"observedGeneration": 0, "phase": "Pending", "endpoint": "", "storageUsedBytes": 0,
			"repositoryCount": 0, "artifactCount": 0, "usageObservedAt": "", "conditions": []any{},
		},
		"resourceVersion": rv, "createdAt": "2026-10-21T10:00:00Z",
	}
	m.registries[name] = reg
	w.Header().Set("ETag", `"`+rv+`"`)
	w.WriteHeader(http.StatusCreated)
	mustEncodeJSON(w, mockRegistryView(reg))
}

// mockRegistryView is the registry as kupe-api returns it.
func mockRegistryView(reg map[string]any) map[string]any {
	view := make(map[string]any, len(reg))
	for k, v := range reg {
		if k != "deleteLag" {
			view[k] = v
		}
	}
	return view
}

func (m *mockKupeAPI) getRegistry(w http.ResponseWriter, name string) {
	reg, ok := m.registries[name]
	if !ok {
		mockNotFound(w, "registry", name)
		return
	}
	if lag, deleting := reg["deleteLag"].(int); deleting {
		if lag <= 0 {
			delete(m.registries, name)
			mockNotFound(w, "registry", name)
			return
		}
		reg["deleteLag"] = lag - 1
		reg["status"].(map[string]any)["phase"] = "Terminating"
		w.Header().Set("ETag", `"`+reg["resourceVersion"].(string)+`"`)
		mustEncodeJSON(w, mockRegistryView(reg))
		return
	}
	capBytes := mockPlanRegistryCapBytes
	if limit := strOrEmpty(reg["storageLimit"]); limit != "" {
		if q, ok := parseSizeQuantity(limit); ok {
			capBytes = q.Num().Int64() / q.Denom().Int64()
		}
	}
	status := map[string]any{
		"observedGeneration": reg["generation"], "phase": "Running", "endpoint": "registry.kupe.cloud/acme",
		"storageUsedBytes": 1048576, "storageLimitBytes": capBytes, "repositoryCount": 2, "artifactCount": 3,
		"usageObservedAt": "2026-10-21T10:05:00Z",
		"conditions":      []any{map[string]any{"type": "Ready", "status": "True", "reason": "RegistryReady"}},
	}
	if len(m.registryStatuses) > 0 {
		for k, v := range m.registryStatuses[0] {
			status[k] = v
		}
		if len(m.registryStatuses) > 1 {
			m.registryStatuses = m.registryStatuses[1:]
		}
	}
	reg["status"] = status
	w.Header().Set("ETag", `"`+reg["resourceVersion"].(string)+`"`)
	mustEncodeJSON(w, mockRegistryView(reg))
}

func (m *mockKupeAPI) patchRegistry(w http.ResponseWriter, r *http.Request, name string) {
	reg, ok := m.registries[name]
	if !ok {
		mockNotFound(w, "registry", name)
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	for key := range body {
		if key != "storageLimit" && key != "deletionProtection" {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	if len(body) == 0 {
		mockError(w, http.StatusBadRequest, "", "at least one field required: storageLimit, deletionProtection")
		return
	}
	_, protection := body["deletionProtection"]
	if protectionOnly := protection && len(body) == 1; m.registriesDisabled && !protectionOnly {
		mockRegistriesUnavailable(w)
		return
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && strings.Trim(ifMatch, `"`) != reg["resourceVersion"] {
		mockError(w, http.StatusPreconditionFailed, "", "resource has been modified (ETag mismatch)")
		return
	}
	if v, ok := body["storageLimit"].(string); ok {
		if v != "" && !mockRegistryStorageLimit.MatchString(v) {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid storageLimit %q", v))
			return
		}
		reg["storageLimit"] = v
	}
	if v, ok := body["deletionProtection"].(bool); ok {
		reg["deletionProtection"] = v
	}
	reg["generation"] = reg["generation"].(int) + 1
	rv := m.nextRV()
	reg["resourceVersion"] = rv
	w.Header().Set("ETag", `"`+rv+`"`)
	mustEncodeJSON(w, mockRegistryView(reg))
}

func (m *mockKupeAPI) deleteRegistry(w http.ResponseWriter, name string) {
	reg, ok := m.registries[name]
	if !ok {
		mockNotFound(w, "registry", name)
		return
	}
	if reg["deletionProtection"] == true {
		mockError(w, http.StatusConflict, client.CodeDeletionProtected,
			fmt.Sprintf("registry %q has deletionProtection enabled: set it to false before deleting", name))
		return
	}
	// Its connections and tokens go with it.
	m.registryBindings = make(map[string]map[string]any)
	m.registryTokens = make(map[string]map[string]any)
	if m.registryDeleteLag > 0 {
		reg["deleteLag"] = m.registryDeleteLag
	} else {
		delete(m.registries, name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Cluster connections ---

func (m *mockKupeAPI) serveRegistryBindings(w http.ResponseWriter, r *http.Request, segs []string) {
	if _, ok := m.registries[segs[0]]; !ok {
		if r.Method == http.MethodPost && m.registriesDisabled {
			mockRegistriesUnavailable(w)
			return
		}
		mockNotFound(w, "registry", segs[0])
		return
	}
	switch {
	case len(segs) == 2 && r.Method == http.MethodPost:
		m.createRegistryBinding(w, r)
	case len(segs) == 3 && r.Method == http.MethodGet:
		m.getRegistryBinding(w, segs[2])
	case len(segs) == 3 && r.Method == http.MethodPatch:
		m.patchRegistryBinding(w, r, segs[2])
	case len(segs) == 3 && r.Method == http.MethodDelete:
		if _, ok := m.registryBindings[segs[2]]; !ok {
			mockNotFound(w, "cluster connection", segs[2])
			return
		}
		delete(m.registryBindings, segs[2])
		w.WriteHeader(http.StatusNoContent)
	default:
		mockError(w, http.StatusMethodNotAllowed, "", "method not allowed")
	}
}

func (m *mockKupeAPI) createRegistryBinding(w http.ResponseWriter, r *http.Request) {
	if m.registriesDisabled {
		mockRegistriesUnavailable(w)
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	for key := range body {
		if !slices.Contains([]string{"cluster", "name", "pushNamespaces", "secretName"}, key) {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	cluster := strOrEmpty(body["cluster"])
	if cluster == "" {
		mockError(w, http.StatusBadRequest, "", "cluster is required")
		return
	}
	if name := strOrEmpty(body["name"]); name != "" && name != cluster {
		mockError(w, http.StatusBadRequest, "", "a cluster connection is named after its cluster")
		return
	}
	push, _ := mockStringList(body["pushNamespaces"])
	if len(push) > 10 {
		mockError(w, http.StatusBadRequest, client.CodeRegistryPushNamespacesLimit,
			"a cluster connection can give at most 10 namespaces a push credential")
		return
	}
	secretName := strOrEmpty(body["secretName"])
	if secretName == "kupe-registry-pull" {
		mockError(w, http.StatusBadRequest, "",
			"secretName kupe-registry-pull is reserved: it is the name of the cluster's pull credential")
		return
	}
	if _, exists := m.registryBindings[cluster]; exists {
		mockError(w, http.StatusConflict, client.CodeRegistryBindingExists,
			fmt.Sprintf("cluster %q is already connected to the registry", cluster))
		return
	}
	rv := m.nextRV()
	binding := map[string]any{
		"name": cluster, "registry": "registry", "cluster": cluster, "pushNamespaces": push,
		"secretName": secretName,
		"status": map[string]any{
			"phase": "", "message": "", "pullSecretName": "", "pullUsername": "", "pushUsername": "",
			"namespaces": []any{}, "rotation": map[string]any{"requestedAt": "", "appliedAt": "", "pending": false},
			"conditions": []any{},
		},
		"resourceVersion": rv, "createdAt": "2026-10-21T10:05:00Z",
	}
	m.registryBindings[cluster] = binding
	w.Header().Set("ETag", `"`+rv+`"`)
	w.WriteHeader(http.StatusCreated)
	mustEncodeJSON(w, mockRegistryBindingView(binding))
}

// mockStringList is a list of strings from a request body; ok is false when
// the field is absent or not a list.
func mockStringList(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return []string{}, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, strOrEmpty(item))
	}
	return out, true
}

func (m *mockKupeAPI) getRegistryBinding(w http.ResponseWriter, name string) {
	binding, ok := m.registryBindings[name]
	if !ok {
		mockNotFound(w, "cluster connection", name)
		return
	}
	push := binding["pushNamespaces"].([]string)
	if lag, _ := binding["lag"].(int); lag > 0 {
		binding["lag"] = lag - 1
		push, _ = binding["delivered"].([]string)
	} else {
		binding["delivered"] = push
	}
	entries := make([]any, 0, len(push))
	for _, ns := range push {
		entries = append(entries, map[string]any{"name": ns, "phase": "Ready", "message": ""})
	}
	pushUsername := ""
	if len(push) > 0 {
		pushUsername = "kupe+acme+push-" + name
	}
	status := map[string]any{
		"phase": "Ready",
		"message": fmt.Sprintf("connected: new pods in cluster %s can pull from registry.kupe.cloud/acme; "+
			"restart workloads started earlier to use it", name),
		"pullSecretName": "kupe-registry-pull", "pullUsername": "kupe+acme+pull-" + name,
		"pushUsername": pushUsername, "namespaces": entries,
		"rotation":   map[string]any{"requestedAt": "", "appliedAt": "", "pending": false},
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ConnectionReady"}},
	}
	for k, v := range m.registryBindingStatus {
		status[k] = v
	}
	binding["status"] = status
	w.Header().Set("ETag", `"`+binding["resourceVersion"].(string)+`"`)
	mustEncodeJSON(w, mockRegistryBindingView(binding))
}

// mockRegistryBindingView is the connection as kupe-api returns it, without
// the mock's own record of what was delivered.
func mockRegistryBindingView(binding map[string]any) map[string]any {
	view := make(map[string]any, len(binding))
	for k, v := range binding {
		if k != "delivered" && k != "lag" {
			view[k] = v
		}
	}
	return view
}

func (m *mockKupeAPI) patchRegistryBinding(w http.ResponseWriter, r *http.Request, name string) {
	binding, ok := m.registryBindings[name]
	if !ok {
		mockNotFound(w, "cluster connection", name)
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	for key := range body {
		if key != "pushNamespaces" {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	push, ok := mockStringList(body["pushNamespaces"])
	if !ok {
		mockError(w, http.StatusBadRequest, "",
			"pushNamespaces is required: send the new list ([] removes every push namespace)")
		return
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && strings.Trim(ifMatch, `"`) != binding["resourceVersion"] {
		mockError(w, http.StatusPreconditionFailed, "", "resource has been modified (ETag mismatch)")
		return
	}
	if m.registriesDisabled {
		for _, ns := range push {
			if !slices.Contains(binding["pushNamespaces"].([]string), ns) {
				mockRegistriesUnavailable(w)
				return
			}
		}
	}
	binding["pushNamespaces"] = push
	binding["lag"] = m.registryBindingLagOnPatch
	rv := m.nextRV()
	binding["resourceVersion"] = rv
	w.Header().Set("ETag", `"`+rv+`"`)
	mustEncodeJSON(w, mockRegistryBindingView(binding))
}

// --- Access tokens ---

func (m *mockKupeAPI) serveRegistryTokens(w http.ResponseWriter, r *http.Request, segs []string) {
	if _, ok := m.registries[segs[0]]; !ok {
		if r.Method == http.MethodPost && m.registriesDisabled {
			mockRegistriesUnavailable(w)
			return
		}
		mockNotFound(w, "registry", segs[0])
		return
	}
	switch {
	case len(segs) == 2 && r.Method == http.MethodPost:
		m.createRegistryToken(w, r)
	case len(segs) == 3 && r.Method == http.MethodGet:
		m.getRegistryToken(w, segs[2])
	case len(segs) == 3 && r.Method == http.MethodDelete:
		if _, ok := m.registryTokens[segs[2]]; !ok {
			mockNotFound(w, "access token", segs[2])
			return
		}
		delete(m.registryTokens, segs[2])
		w.WriteHeader(http.StatusNoContent)
	default:
		mockError(w, http.StatusMethodNotAllowed, "", "method not allowed")
	}
}

// mockTokenSecret is the secret the mock gives token id: kupecr_ and 42
// characters, like the platform's.
func mockTokenSecret(id string) string {
	return "kupecr_" + strings.Repeat("S", 26) + strings.TrimPrefix(id, "rt-")
}

func (m *mockKupeAPI) createRegistryToken(w http.ResponseWriter, r *http.Request) {
	if m.registriesDisabled {
		mockRegistriesUnavailable(w)
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	for key := range body {
		if !slices.Contains([]string{"displayName", "access", "expiresInDays"}, key) {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	access := strOrEmpty(body["access"])
	if access != "pull" && access != "push" {
		mockError(w, http.StatusBadRequest, "", "access must be pull or push")
		return
	}
	days := 90
	if v, ok := body["expiresInDays"].(float64); ok {
		if v < 1 || v > 365 {
			mockError(w, http.StatusBadRequest, client.CodeRegistryTokenExpiryInvalid, "expiresInDays must be between 1 and 365")
			return
		}
		days = int(v)
	}
	if len(m.registryTokens) >= m.registryTokenLimit {
		mockError(w, http.StatusConflict, client.CodeRegistryTokenLimitReached,
			fmt.Sprintf("the registry already has %d access tokens; revoke one first", m.registryTokenLimit))
		return
	}
	id := fmt.Sprintf("rt-%016x", 0xa0+m.rvCounter)
	m.rvCounter++
	createdAt := "2026-10-21T10:00:00Z"
	expiresAt := mockAddDays(createdAt, days)
	predicted := "kupe+acme+tok-" + strings.TrimPrefix(id, "rt-")
	token := map[string]any{
		"id": id, "displayName": body["displayName"], "kind": "token", "access": access,
		"createdBy": "admin@acme.com", "createdAt": createdAt, "expiresAt": expiresAt,
		"username": "", "lastUsedAt": "",
		"status": map[string]any{"phase": "", "message": "", "conditions": []any{}},
		// predicted is the mock's record of the username in the create
		// response; the API returns status.username only.
		"predicted": predicted,
	}
	m.registryTokens[id] = token
	w.WriteHeader(http.StatusCreated)
	mustEncodeJSON(w, map[string]any{
		"id": id, "displayName": body["displayName"], "kind": "token", "access": access,
		"createdBy": "admin@acme.com", "createdAt": createdAt, "expiresAt": expiresAt,
		"registry": "registry.kupe.cloud", "username": predicted, "secret": mockTokenSecret(id),
		"status": map[string]any{"phase": ""},
	})
}

// mockAddDays is the RFC 3339 time days after start.
func mockAddDays(start string, days int) string {
	t, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	return t.AddDate(0, 0, days).Format(time.RFC3339)
}

func (m *mockKupeAPI) getRegistryToken(w http.ResponseWriter, id string) {
	token, ok := m.registryTokens[id]
	if !ok {
		mockNotFound(w, "access token", id)
		return
	}
	phase := "Ready"
	if len(m.registryTokenPhases) > 0 {
		phase = m.registryTokenPhases[0]
		if len(m.registryTokenPhases) > 1 {
			m.registryTokenPhases = m.registryTokenPhases[1:]
		}
	}
	status := token["status"].(map[string]any)
	status["phase"] = phase
	if phase == "Failed" {
		status["message"] = "the access token could not be issued; create a new one"
	}
	if phase == "Ready" && token["username"] == "" {
		token["username"] = token["predicted"]
		if m.registryTokenIssuedUsername != "" {
			token["username"] = m.registryTokenIssuedUsername
		}
	}
	view := make(map[string]any, len(token))
	for k, v := range token {
		if k != "predicted" {
			view[k] = v
		}
	}
	mustEncodeJSON(w, view)
}
