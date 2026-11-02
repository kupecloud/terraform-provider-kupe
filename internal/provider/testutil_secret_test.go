package provider

import (
	"net/http"
	"strings"
)

// Scripted statuses overlay a delivered observation; the last repeats. PATCH
// returns the previous status, like kupe-api while the controller catches up.
type mockSecretState struct {
	secretStatuses        []map[string]any
	secretStatusesOnPatch []map[string]any
	secretBeforePatch     []func(map[string]any)
	secretRequests        []mockRequest
	secretGets            int
	secretGetErrorStatus  int
	// Positive: GETs before 404; -1: withdrawal never finishes.
	secretDeleteLag int
	vaultEntries    map[string]*mockVaultEntry
	vaultRequests   []mockVaultRequest
	vaultBeforePut  func()
	// Runs once only after a value PUT is confirmed, so a planning read
	// cannot consume a fault intended for the following delivery request.
	vaultAfterPut            func()
	vaultFaults              []mockVaultFault
	vaultReadStatus          int
	vaultReadCode            string
	vaultReadValues          bool
	vaultSkipStamp           bool
	secretRefreshes          int
	secretRefreshErrorStatus int
}

func (m *mockKupeAPI) handleSecret(w http.ResponseWriter, r *http.Request) {
	name := lastSegment(r.URL.Path)
	s, exists := m.secrets[name]
	switch r.Method {
	case http.MethodPost:
		var body map[string]any
		mustDecodeJSON(r, &body)
		name = strOrEmpty(body["name"])
		if _, exists := m.secrets[name]; exists {
			mockError(w, http.StatusConflict, "", "secret already exists")
			return
		}
		m.secretRequests = append(m.secretRequests, mockRequest{method: r.Method, body: body})
		s = map[string]any{"name": name, "secretPath": body["secretPath"], "sync": sliceOrEmpty(body["sync"]),
			"generation": 1, "status": map[string]any{"phase": "Pending", "observedGeneration": 0},
			"resourceVersion": m.nextRV(), "createdAt": "2024-01-01T00:00:00Z"}
		m.secrets[name] = s
		w.Header().Set("ETag", `"`+s["resourceVersion"].(string)+`"`)
		w.WriteHeader(http.StatusCreated)
		mustEncodeJSON(w, s)
	case http.MethodGet:
		m.secretGets++
		if m.secretGetErrorStatus != 0 {
			mockError(w, m.secretGetErrorStatus, "", "secret read refused")
			return
		}
		if exists {
			if remaining, deleting := s["_deleteRemaining"].(int); deleting {
				if remaining == 0 {
					delete(m.secrets, name)
					exists = false
				} else {
					if remaining > 0 {
						s["_deleteRemaining"] = remaining - 1
					}
					s["status"] = map[string]any{"phase": "Deleting", "observedGeneration": s["generation"]}
				}
			} else {
				status := mockSecretDeliveredStatus(s)
				if len(m.secretStatuses) > 0 {
					for k, v := range m.secretStatuses[0] {
						status[k] = v
					}
					if len(m.secretStatuses) > 1 {
						m.secretStatuses = m.secretStatuses[1:]
					}
				}
				s["status"] = status
			}
		}
		if !exists {
			mockError(w, http.StatusNotFound, "", "not found")
			return
		}
		w.Header().Set("ETag", `"`+s["resourceVersion"].(string)+`"`)
		mustEncodeJSON(w, s)
	case http.MethodPatch:
		if !exists {
			mockError(w, http.StatusNotFound, "", "not found")
			return
		}
		var body map[string]any
		mustDecodeJSON(r, &body)
		m.secretRequests = append(m.secretRequests, mockRequest{method: r.Method, ifMatch: r.Header.Get("If-Match"), body: body})
		if len(m.secretBeforePatch) > 0 {
			m.secretBeforePatch[0](s)
			m.secretBeforePatch = m.secretBeforePatch[1:]
			s["resourceVersion"] = m.nextRV()
		}
		if strings.Trim(r.Header.Get("If-Match"), `"`) != s["resourceVersion"] {
			mockError(w, http.StatusPreconditionFailed, "", "resource has been modified (ETag mismatch)")
			return
		}
		s["sync"] = sliceOrEmpty(body["sync"])
		s["generation"] = s["generation"].(int) + 1
		s["resourceVersion"] = m.nextRV()
		if m.secretStatusesOnPatch != nil {
			m.secretStatuses = m.secretStatusesOnPatch
		}
		w.Header().Set("ETag", `"`+s["resourceVersion"].(string)+`"`)
		mustEncodeJSON(w, s)
	case http.MethodDelete:
		if !exists {
			mockError(w, http.StatusNotFound, "", "not found")
			return
		}
		if m.secretDeleteLag == 0 {
			delete(m.secrets, name)
		} else {
			s["_deleteRemaining"] = m.secretDeleteLag
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		mockError(w, http.StatusMethodNotAllowed, "", "method not allowed")
	}
}

func mockSecretDeliveredStatus(s map[string]any) map[string]any {
	statuses := []any{}
	for _, elem := range s["sync"].([]any) {
		target := elem.(map[string]any)
		name := strOrEmpty(target["secretName"])
		if name == "" {
			name = strOrEmpty(s["name"])
		}
		statuses = append(statuses, map[string]any{"cluster": target["cluster"], "namespace": target["namespace"],
			"secretName": name, "phase": "Synced", "reason": "Delivered", "deliveredRefresh": s["_refresh"]})
	}
	return map[string]any{"phase": "Active", "observedGeneration": s["generation"], "syncStatuses": statuses,
		"refresh":    map[string]any{"requestedAt": s["_refresh"], "observedAt": s["_refresh"], "pending": false},
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Delivered"}}}
}
