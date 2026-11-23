package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// The cluster access token part of mockKupeAPI. It answers like kupe-api
// handler_cluster_access_token.go (kupe-api main after PR 284): every route
// reads the cluster first; create returns no token; the token is issued
// (phase Ready) on the first GET; the collect serves the token and a
// kubeconfig once, to its creator. Called with m.mu held.

const mockClustersPath = "/api/v1/tenants/acme/clusters"

// mockCollectAnswer is a scripted collect refusal.
type mockCollectAnswer struct {
	status int
	code   string
}

// mockClusterAccessTokenState is the cluster access token half of the mock.
type mockClusterAccessTokenState struct {
	// catTokens is keyed by id.
	catTokens map[string]map[string]any
	// catPhases, when set, are the phases the next token GETs report in
	// turn, the last one repeating (default Ready).
	catPhases []string
	// catCollectAnswers, when set, answer the next collects in turn before
	// the mock serves one.
	catCollectAnswers []mockCollectAnswer
	// catRevokeOnCollect deletes the token when it is collected, as a revoke
	// landing during the collect does: the answer is 404.
	catRevokeOnCollect bool
	// catLoseCollectAfterClaim claims the token at its collect and then
	// answers 500, as kupe-api does when it cannot build the kubeconfig
	// after the claim: the token is lost.
	catLoseCollectAfterClaim bool
	// catRevokeAfterGet deletes the token once a GET has answered, as a
	// revoke landing during the wait does: later requests answer 404.
	catRevokeAfterGet bool
	// catRoutesMissing answers every access token route like a kupe-api
	// older than them: the HTTP router's plain-text 404.
	catRoutesMissing bool
	// catUncodedNotFound answers 404s without their codes, as a kupe-api
	// older than them does.
	catUncodedNotFound bool
	// catRequests records every access token request in order.
	catRequests []mockRegistryRequest
}

func newMockClusterAccessTokenState() mockClusterAccessTokenState {
	return mockClusterAccessTokenState{catTokens: make(map[string]map[string]any)}
}

// mockClusterAccessToken is the token the mock serves for id.
func mockClusterAccessToken(id string) string {
	return "eyJhbGciOiJSUzI1NiJ9.mock-" + strings.TrimPrefix(id, "cat-") + ".c2lnbmF0dXJl"
}

// mockClusterAccessKubeconfig is the kubeconfig the mock serves for id.
func mockClusterAccessKubeconfig(cluster, id string) string {
	name := "kupe-acme-" + cluster + "-" + id
	return fmt.Sprintf(`apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Q0E=
    server: https://%[1]s.acme.kupe.cloud
  name: %[2]s
contexts:
- context:
    cluster: %[2]s
    user: %[2]s
  name: %[2]s
current-context: %[2]s
kind: Config
users:
- name: %[2]s
  user:
    token: %[3]s
`, cluster, name, mockClusterAccessToken(id))
}

// isMockClusterAccessTokenPath reports whether path is under
// /clusters/{name}/access-tokens.
func isMockClusterAccessTokenPath(path string) bool {
	segs := strings.Split(strings.Trim(strings.TrimPrefix(path, mockClustersPath), "/"), "/")
	return strings.HasPrefix(path, mockClustersPath+"/") && len(segs) >= 2 && segs[1] == "access-tokens"
}

// serveClusterAccessTokens routes /clusters/{name}/access-tokens[/{id}[/token]].
func (m *mockKupeAPI) serveClusterAccessTokens(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var logged map[string]any
	_ = json.Unmarshal(raw, &logged)
	m.catRequests = append(m.catRequests, mockRegistryRequest{method: r.Method, path: r.URL.Path, body: logged})
	if m.catRoutesMissing {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "404 page not found\n")
		return
	}

	segs := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, mockClustersPath), "/"), "/")
	cluster := segs[0]
	if _, ok := m.clusters[cluster]; !ok {
		m.catNotFound(w, "cluster", cluster, client.CodeClusterNotFound)
		return
	}
	var token map[string]any
	if len(segs) >= 3 {
		token = m.catTokens[segs[2]]
		if token == nil || token["cluster"] != cluster {
			m.catNotFound(w, "access token", segs[2], client.CodeAccessTokenNotFound)
			return
		}
	}
	switch {
	case len(segs) == 2 && r.Method == http.MethodPost:
		m.createClusterAccessToken(w, r, cluster)
	case len(segs) == 3 && r.Method == http.MethodGet:
		m.getClusterAccessToken(w, token)
		if m.catRevokeAfterGet {
			delete(m.catTokens, segs[2])
		}
	case len(segs) == 3 && r.Method == http.MethodDelete:
		delete(m.catTokens, segs[2])
		w.WriteHeader(http.StatusNoContent)
	case len(segs) == 4 && segs[3] == "token" && r.Method == http.MethodPost:
		m.collectClusterAccessToken(w, token)
	default:
		mockError(w, http.StatusMethodNotAllowed, "", "method not allowed")
	}
}

func (m *mockKupeAPI) createClusterAccessToken(w http.ResponseWriter, r *http.Request, cluster string) {
	var body map[string]any
	mustDecodeJSON(r, &body)
	for key := range body {
		if !slices.Contains([]string{"displayName", "access", "expiresInDays"}, key) {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	access, _ := body["access"].(map[string]any)
	if access == nil || access["role"] != "admin" || len(access) != 1 {
		mockError(w, http.StatusBadRequest, "", "access.role must be admin")
		return
	}
	days := 90
	if v, ok := body["expiresInDays"].(float64); ok {
		if v < 1 || v > 365 {
			mockFieldError(w, http.StatusBadRequest, client.CodeAccessTokenExpiryInvalid, "spec.expiresAt",
				"expiresInDays must be between 1 and 365")
			return
		}
		days = int(v)
	}
	id := fmt.Sprintf("cat-%016x", 0xc0+m.rvCounter)
	m.rvCounter++
	createdAt := "2026-11-22T10:00:00Z"
	token := map[string]any{
		"id": id, "cluster": cluster, "displayName": body["displayName"],
		"access":    map[string]any{"role": "admin"},
		"createdBy": "admin@acme.com", "createdAt": createdAt, "expiresAt": mockAddDays(createdAt, days),
		"collectedAt": "", "collected": false,
		"status": map[string]any{"phase": "Pending", "message": "", "conditions": []any{}},
	}
	m.catTokens[id] = token
	w.WriteHeader(http.StatusCreated)
	mustEncodeJSON(w, map[string]any{
		"id": id, "cluster": cluster, "displayName": body["displayName"], "access": token["access"],
		"createdBy": token["createdBy"], "createdAt": createdAt, "expiresAt": token["expiresAt"],
		"collected": false, "status": map[string]any{"phase": "Pending"},
	})
}

func (m *mockKupeAPI) getClusterAccessToken(w http.ResponseWriter, token map[string]any) {
	phase := "Ready"
	if len(m.catPhases) > 0 {
		phase = m.catPhases[0]
		if len(m.catPhases) > 1 {
			m.catPhases = m.catPhases[1:]
		}
	}
	status := token["status"].(map[string]any)
	status["phase"] = phase
	if phase == "Failed" {
		status["message"] = "the access token could not be issued; revoke it and create another"
	}
	mustEncodeJSON(w, token)
}

func (m *mockKupeAPI) collectClusterAccessToken(w http.ResponseWriter, token map[string]any) {
	id := token["id"].(string)
	if len(m.catCollectAnswers) > 0 {
		answer := m.catCollectAnswers[0]
		m.catCollectAnswers = m.catCollectAnswers[1:]
		mockError(w, answer.status, answer.code, "refused by the mock: "+answer.code)
		return
	}
	if m.catRevokeOnCollect {
		delete(m.catTokens, id)
		m.catNotFound(w, "access token", id, client.CodeAccessTokenNotFound)
		return
	}
	switch token["status"].(map[string]any)["phase"] {
	case "Failed":
		mockError(w, http.StatusConflict, client.CodeAccessTokenFailed,
			"the access token could not be issued; revoke it and create another")
		return
	case "Ready":
	default:
		mockError(w, http.StatusConflict, client.CodeAccessTokenNotReady,
			"the access token is not ready yet; try again in a few seconds")
		return
	}
	if token["collected"] == true {
		mockError(w, http.StatusGone, client.CodeAccessTokenAlreadyCollected,
			"the access token was already collected; it is shown only once. If it was lost, revoke it and create another")
		return
	}
	token["collected"] = true
	token["collectedAt"] = "2026-11-22T10:00:05Z"
	if m.catLoseCollectAfterClaim {
		mockError(w, http.StatusInternalServerError, "", "internal server error")
		return
	}
	cluster := token["cluster"].(string)
	w.Header().Set("Cache-Control", "no-store")
	mustEncodeJSON(w, map[string]any{
		"id": id, "cluster": cluster,
		"token":                mockClusterAccessToken(id),
		"kubeconfig":           mockClusterAccessKubeconfig(cluster, id),
		"endpoint":             "https://" + cluster + ".acme.kupe.cloud",
		"certificateAuthority": "Q0E=",
		"expiresAt":            token["expiresAt"], "expirationTimestamp": token["expiresAt"],
	})
}

// mockCATRequestsOf are the access token requests of method to path, in
// order.
func (m *mockKupeAPI) mockCATRequestsOf(method, path string) []mockRegistryRequest {
	var out []mockRegistryRequest
	for _, req := range m.catRequests {
		if req.method == method && req.path == path {
			out = append(out, req)
		}
	}
	return out
}

// catNotFound is kupe-api's 404 for a cluster or token: coded, or only
// named when catUncodedNotFound is set.
func (m *mockKupeAPI) catNotFound(w http.ResponseWriter, noun, name, code string) {
	if m.catUncodedNotFound {
		code = ""
	}
	mockError(w, http.StatusNotFound, code, fmt.Sprintf("%s %q not found", noun, name))
}
