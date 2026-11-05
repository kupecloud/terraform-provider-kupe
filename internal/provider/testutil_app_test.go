package provider

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

const mockAppsPath = "/api/v1/tenants/acme/apps"
const mockAppImagePath = "/api/v1/tenants/acme/app-images/resolve"

type mockAppState struct {
	mockAppEnvironmentState
	apps                map[string]map[string]any
	appsDisabled        bool
	appReadsUnavailable bool
	appRequests         []mockRequest
	appStatuses         []map[string]any
	appDeleteLag        int
	appBeforePatch      []func(map[string]any)
	appOnGet            []func(map[string]any)
	appImages           map[string]client.ImageResolution
	appImageRequests    []string
	appImageRefusal     *mockRefusal
	appImageRetryAfter  string
}

func newMockAppState() mockAppState {
	return mockAppState{apps: map[string]map[string]any{}, appImages: map[string]client.ImageResolution{}, mockAppEnvironmentState: newMockAppEnvironmentState()}
}

// Image resolution is a read-only POST, served under the existing mock lock.
func (m *mockKupeAPI) serveAppImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Image string `json:"image"`
	}
	mustDecodeJSON(r, &body)
	m.appImageRequests = append(m.appImageRequests, body.Image)
	if m.appsDisabled {
		mockError(w, http.StatusNotFound, client.CodeAppsUnavailable, "apps are not available on this platform")
		return
	}
	if refusal := m.appImageRefusal; refusal != nil {
		if m.appImageRetryAfter != "" {
			w.Header().Set("Retry-After", m.appImageRetryAfter)
		}
		mockError(w, refusal.status, refusal.code, refusal.message)
		return
	}
	image, ok := m.appImages[body.Image]
	if !ok {
		mockError(w, http.StatusNotFound, client.CodeImageNotFound, "image not found or not public")
		return
	}
	mustEncodeJSON(w, image)
}

// serveApps runs under the existing mock's lock, with UID.generation ETags.
func (m *mockKupeAPI) serveApps(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, mockAppsPath), "/")
	name := ""
	if len(parts) > 1 {
		name = parts[1]
	}
	if len(parts) > 2 {
		m.serveAppEnvironment(w, r, name, parts[2:])
		return
	}
	var body map[string]any
	if r.Body != nil && r.ContentLength != 0 {
		mustDecodeJSON(r, &body)
	}
	m.appRequests = append(m.appRequests, mockRequest{method: r.Method, ifMatch: r.Header.Get("If-Match"), body: body})
	if m.appReadsUnavailable && r.Method == http.MethodGet || m.appsDisabled && r.Method == http.MethodPost {
		mockError(w, http.StatusNotFound, client.CodeAppsUnavailable, "apps are not available on this platform")
		return
	}
	if r.Method == http.MethodPost && name == "" {
		name = body["name"].(string)
		if _, exists := m.apps[name]; exists {
			mockError(w, http.StatusConflict, "", "app already exists")
			return
		}
		app := map[string]any{"name": name, "uid": "app-" + m.nextRV(), "generation": 1, "createdAt": "2026-01-01T00:00:00Z", "deletionProtection": body["deletionProtection"] != false, "status": map[string]any{"phase": "Pending", "observedGeneration": 0}}
		m.apps[name] = app
		w.Header().Set("ETag", mockAppETag(app))
		w.WriteHeader(http.StatusCreated)
		mustEncodeJSON(w, app)
		return
	}
	app, exists := m.apps[name]
	if !exists {
		mockNotFound(w, "app", name)
		return
	}
	if r.Method == http.MethodGet {
		if len(m.appOnGet) > 0 {
			m.appOnGet[0](app)
			if len(m.appOnGet) > 1 {
				m.appOnGet = m.appOnGet[1:]
			}
		}
		if app["deleting"] == true {
			if m.appDeleteLag == 0 {
				delete(m.apps, name)
				mockNotFound(w, "app", name)
				return
			}
			m.appDeleteLag--
			app["status"] = map[string]any{"phase": "Terminating", "observedGeneration": app["generation"]}
		} else {
			status := map[string]any{"phase": "Running", "observedGeneration": app["generation"]}
			if len(m.appStatuses) > 0 {
				for key, value := range m.appStatuses[0] {
					status[key] = value
				}
				if len(m.appStatuses) > 1 {
					m.appStatuses = m.appStatuses[1:]
				}
			}
			app["status"] = status
		}
		w.Header().Set("ETag", mockAppETag(app))
		mustEncodeJSON(w, app)
		return
	}
	if r.Method == http.MethodPatch && len(m.appBeforePatch) > 0 {
		hook := m.appBeforePatch[0]
		m.appBeforePatch = m.appBeforePatch[1:]
		hook(app)
	}
	if r.Header.Get("If-Match") != mockAppETag(app) {
		mockError(w, http.StatusPreconditionFailed, "precondition_failed", "the app changed")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		app["deletionProtection"] = body["deletionProtection"]
		app["generation"] = app["generation"].(int) + 1
		w.Header().Set("ETag", mockAppETag(app))
		mustEncodeJSON(w, app)
	case http.MethodDelete:
		if body["confirm"] != name {
			mockError(w, http.StatusBadRequest, "confirm_mismatch", "confirmation does not match")
			return
		}
		if app["deletionProtection"] == true {
			mockError(w, http.StatusConflict, client.CodeDeletionProtected, "turn deletion protection off first")
			return
		}
		for _, env := range m.appEnvironments {
			if env.App == name {
				mockError(w, http.StatusPreconditionFailed, "precondition_failed", "the app has remaining environments")
				return
			}
		}
		if children, ok := body["environments"].([]any); !ok || len(children) != 0 {
			mockError(w, http.StatusPreconditionFailed, "precondition_failed", "review an empty environment set")
			return
		}
		app["deleting"] = true
		w.WriteHeader(http.StatusAccepted)
		mustEncodeJSON(w, map[string]any{"status": map[string]any{"phase": "Terminating"}})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func mockAppETag(app map[string]any) string {
	return fmt.Sprintf(`"%s.%d"`, app["uid"], app["generation"])
}
