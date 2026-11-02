package provider

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

type mockVaultEntry struct {
	version int64
	values  map[string]string
	deleted bool
}

type mockVaultRequest struct {
	ifMatch, ifNoneMatch string
	values               map[string]string
}

type mockVaultFault struct {
	status             int
	code               string
	disconnect, commit bool
}

func (m *mockKupeAPI) handleVault(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/tenants/acme/vault/")
	entry := m.vaultEntries[path]
	if r.Method == http.MethodGet {
		m.vaultReadValues = m.vaultReadValues || r.URL.Query().Has("values")
		if m.vaultReadStatus != 0 {
			mockError(w, m.vaultReadStatus, m.vaultReadCode, "metadata read unavailable")
			return
		}
		if entry == nil {
			mockError(w, http.StatusNotFound, "", "entry absent")
			return
		}
		refs := []string{}
		for name, secret := range m.secrets {
			if secret["secretPath"] == path {
				refs = append(refs, name)
			}
		}
		mustEncodeJSON(w, map[string]any{"path": path, "version": entry.version, "deleted": entry.deleted, "references": refs})
		return
	}
	if r.Method != http.MethodPut {
		mockError(w, http.StatusMethodNotAllowed, "", "method refused")
		return
	}
	var body struct {
		Values map[string]string `json:"values"`
	}
	mustDecodeJSON(r, &body)
	m.vaultRequests = append(m.vaultRequests, mockVaultRequest{ifMatch: r.Header.Get("If-Match"), ifNoneMatch: r.Header.Get("If-None-Match"), values: body.Values})
	if m.vaultBeforePut != nil {
		fn := m.vaultBeforePut
		m.vaultBeforePut = nil
		fn()
		entry = m.vaultEntries[path]
	}
	match, none := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if (match == "") == (none == "") {
		mockError(w, http.StatusPreconditionRequired, "precondition_required", "one condition required")
		return
	}
	if (none != "" && (none != "*" || entry != nil)) || (match != "" && (entry == nil || match != fmt.Sprintf("\"%d\"", entry.version))) {
		mockError(w, http.StatusPreconditionFailed, client.CodeSecretVersionConflict, "entry changed")
		return
	}
	var fault mockVaultFault
	if len(m.vaultFaults) > 0 {
		fault = m.vaultFaults[0]
		m.vaultFaults = m.vaultFaults[1:]
	}
	version := int64(1)
	if entry != nil {
		version = entry.version + 1
	}
	if fault.status == 0 || fault.commit {
		if m.vaultEntries == nil {
			m.vaultEntries = make(map[string]*mockVaultEntry)
		}
		m.vaultEntries[path] = &mockVaultEntry{version: version, values: body.Values}
	}
	if fault.disconnect {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		_ = conn.Close()
		return
	}
	if fault.status != 0 {
		mockError(w, fault.status, fault.code, "test-secret-sentinel-must-not-appear")
		return
	}
	refreshed := []string{}
	if !m.vaultSkipStamp {
		for name, secret := range m.secrets {
			if secret["secretPath"] == path {
				secret["_refresh"] = "refresh-" + m.nextRV()
				secret["resourceVersion"] = m.nextRV()
				refreshed = append(refreshed, name)
			}
		}
	}
	if m.vaultAfterPut != nil {
		after := m.vaultAfterPut
		m.vaultAfterPut = nil
		after()
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", version))
	mustEncodeJSON(w, map[string]any{"path": path, "version": version, "refreshRequested": refreshed})
}

func (m *mockKupeAPI) handleSecretRefresh(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tenants/acme/secrets/"), "/refresh")
	secret := m.secrets[name]
	if secret == nil {
		mockError(w, http.StatusNotFound, "", "definition absent")
		return
	}
	m.secretRefreshes++
	if m.secretRefreshErrorStatus != 0 {
		mockError(w, m.secretRefreshErrorStatus, "", "refresh refused")
		return
	}
	token := "refresh-" + m.nextRV()
	secret["_refresh"] = token
	secret["resourceVersion"] = m.nextRV()
	w.WriteHeader(http.StatusAccepted)
	mustEncodeJSON(w, map[string]any{"name": name, "requestedAt": token})
}
