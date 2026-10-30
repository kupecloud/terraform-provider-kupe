package provider

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// The manual backup and restore part of mockKupeAPI. It answers like
// kupe-api stream/mdb2 (MDB2-1v): POST /databases/{name}/backups takes a
// backup, /backups/{backup} reads and deletes one, and a database POST with
// `source` restores. The platform's restore checks (MDB2-1u, not built yet)
// follow 1v's recorded assumptions. Called with m.mu held.

const mockBackupsPath = "/api/v1/tenants/acme/backups"

// mockBackupLimit is the most manual backups a database keeps.
const mockBackupLimit = 20

// mockBackupTime is the time in the name kupe-api gives an unnamed backup.
const mockBackupTime = "20261030-141500"

// The restore window the mock reports for a database with backup settings
// (getDatabase).
const (
	mockRecoveryWindowEarliest = "2026-10-14T03:17:00Z"
	mockRecoveryWindowLatest   = "2026-10-21T09:00:00Z"
)

// mockBackupState is the manual backup half of the mock's state.
type mockBackupState struct {
	// backups is keyed by name. A backup outlives its database: deleting a
	// database leaves them.
	backups map[string]map[string]any
	// backupSources is the database each backup was taken of, as it was
	// then, keyed by backup name.
	backupSources map[string]map[string]any
	// backupStatuses, when set, are laid over the Completed status of the
	// next backup GETs in turn, the last one repeating.
	backupStatuses []map[string]any
	// backupGets counts backup GETs.
	backupGets int
	// backupRequests records each backup POST body.
	backupRequests []mockRequest
	// backupsNotServed answers like a platform with no manual backups at
	// all (the backup kind is not installed): a backup POST and a restore
	// are 404 backups_unavailable, and a backup GET a plain 404.
	backupsNotServed bool
	// backupRefusal, when set, refuses the next backup POST, then clears.
	backupRefusal *mockRefusal
}

func newMockBackupState() mockBackupState {
	return mockBackupState{backups: map[string]map[string]any{}, backupSources: map[string]map[string]any{}}
}

// serveBackups routes /backups/{backup}. Reads and deletes are served
// whatever the platform's switches say, so backups can always be found and
// deleted.
func (m *mockKupeAPI) serveBackups(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, mockBackupsPath), "/")
	if name == "" || strings.Contains(name, "/") {
		mockError(w, http.StatusNotFound, "", "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		m.getBackup(w, name)
	case http.MethodDelete:
		m.deleteBackup(w, name)
	default:
		mockError(w, http.StatusMethodNotAllowed, "", "method not allowed")
	}
}

// createBackup takes a manual backup of database, checking in kupe-api's
// order.
func (m *mockKupeAPI) createBackup(w http.ResponseWriter, r *http.Request, database string) {
	if refusal := m.backupRefusal; refusal != nil {
		m.backupRefusal = nil
		mockError(w, refusal.status, refusal.code, refusal.message)
		return
	}
	if m.creationPaused {
		mockError(w, http.StatusBadRequest, client.CodeCreationPaused,
			"Database creation is temporarily paused while we expand platform capacity. Please try again later.")
		return
	}
	if m.backupsUnavailable {
		mockError(w, http.StatusBadRequest, client.CodeBackupsUnavailable, "backups are not available on this platform")
		return
	}
	var body map[string]any
	mustDecodeJSON(r, &body)
	m.backupRequests = append(m.backupRequests, mockRequest{method: r.Method, body: body})
	for key := range body {
		if key != "name" {
			mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid request body: json: unknown field %q", key))
			return
		}
	}
	name := strOrEmpty(body["name"])
	if name == "" {
		name = database + "-" + mockBackupTime
	}
	if len(name) > maxNameLength || !namespacePattern.MatchString(name) {
		mockError(w, http.StatusBadRequest, "", fmt.Sprintf("invalid backup name %q", name))
		return
	}
	db, ok := m.databases[database]
	if !ok {
		mockError(w, http.StatusNotFound, "", fmt.Sprintf("database %q not found", database))
		return
	}
	if m.backupsNotServed {
		mockError(w, http.StatusNotFound, client.CodeBackupsUnavailable, "backups are not available on this platform")
		return
	}
	if status, _ := db["status"].(map[string]any); status["phase"] != databasePhaseRunning {
		mockError(w, http.StatusConflict, client.CodeDatabaseNotReady, fmt.Sprintf("database %q is not running", database))
		return
	}
	if _, exists := m.backups[name]; exists {
		mockError(w, http.StatusConflict, "", fmt.Sprintf("database backup %q already exists", name))
		return
	}
	count := 0
	for _, b := range m.backups {
		if b["database"] == database {
			count++
		}
	}
	if count >= mockBackupLimit {
		mockError(w, http.StatusConflict, client.CodeBackupLimitReached,
			fmt.Sprintf("database %q already has %d manual backups", database, mockBackupLimit))
		return
	}
	resources, _ := db["resources"].(map[string]any)
	m.backupSources[name] = map[string]any{"name": database, "version": db["version"], "storage": resources["storage"]}
	rv := m.nextRV()
	backup := map[string]any{
		"name": name, "database": database, "reason": "Manual",
		"status": map[string]any{
			"phase": "Pending", "completedAt": "", "sizeBytes": 0, "source": nil, "databaseDeleted": false,
			"conditions": []any{},
		},
		"resourceVersion": rv, "createdAt": "2026-10-30T14:15:00Z",
	}
	m.backups[name] = backup
	w.Header().Set("ETag", `"`+rv+`"`)
	w.WriteHeader(http.StatusCreated)
	mustEncodeJSON(w, backup)
}

// getBackup answers a backup Completed unless backupStatuses says otherwise.
func (m *mockKupeAPI) getBackup(w http.ResponseWriter, name string) {
	backup, ok := m.backups[name]
	if !ok {
		mockError(w, http.StatusNotFound, "", fmt.Sprintf("database backup %q not found", name))
		return
	}
	m.backupGets++
	status := map[string]any{
		"phase": "Completed", "completedAt": "2026-10-30T14:17:00Z", "sizeBytes": int64(1073741824),
		"source":     m.backupSources[name],
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Completed"}},
	}
	if len(m.backupStatuses) > 0 {
		for k, v := range m.backupStatuses[0] {
			status[k] = v
		}
		if len(m.backupStatuses) > 1 {
			m.backupStatuses = m.backupStatuses[1:]
		}
	}
	_, live := m.databases[strOrEmpty(backup["database"])]
	status["databaseDeleted"] = !live
	backup["status"] = status
	w.Header().Set("ETag", `"`+backup["resourceVersion"].(string)+`"`)
	mustEncodeJSON(w, backup)
}

func (m *mockKupeAPI) deleteBackup(w http.ResponseWriter, name string) {
	if _, ok := m.backups[name]; !ok {
		mockError(w, http.StatusNotFound, "", fmt.Sprintf("database backup %q not found", name))
		return
	}
	delete(m.backups, name)
	delete(m.backupSources, name)
	w.WriteHeader(http.StatusNoContent)
}

// mockRestore checks a database POST's `source` the way kupe-api and the
// platform's restore checks do, and returns the major version the new
// database runs and the source as kupe-api stores and echoes it
// (pointInTime in UTC). It answers a refusal itself and returns false.
func (m *mockKupeAPI) mockRestore(w http.ResponseWriter, body map[string]any) (string, map[string]any, bool) {
	sent, _ := body["source"].(map[string]any)
	if m.backupsUnavailable {
		mockFieldError(w, http.StatusBadRequest, client.CodeBackupsUnavailable, "spec.source",
			"backups are not available on this platform")
		return "", nil, false
	}
	backupName, database, pointInTime := strOrEmpty(sent["backup"]), strOrEmpty(sent["database"]), strOrEmpty(sent["pointInTime"])
	if (backupName == "") == (database == "") {
		mockFieldError(w, http.StatusBadRequest, "", "spec.source", "source must set exactly one of backup and database")
		return "", nil, false
	}
	if pointInTime != "" && database == "" {
		mockFieldError(w, http.StatusBadRequest, "", "spec.source.pointInTime",
			"source.pointInTime can only be set with source.database")
		return "", nil, false
	}
	stored := map[string]any{}
	if pointInTime != "" {
		at, err := time.Parse(time.RFC3339, pointInTime)
		if err != nil {
			mockFieldError(w, http.StatusBadRequest, "", "spec.source.pointInTime",
				fmt.Sprintf("invalid source.pointInTime %q", pointInTime))
			return "", nil, false
		}
		stored["pointInTime"] = at.UTC().Format(time.RFC3339)
	}
	if m.backupsNotServed {
		mockError(w, http.StatusNotFound, client.CodeBackupsUnavailable, "backups are not available on this platform")
		return "", nil, false
	}

	var version, storage string
	if backupName != "" {
		stored["backup"] = backupName
		backup, ok := m.backups[backupName]
		if !ok {
			mockFieldError(w, http.StatusBadRequest, client.CodeRestoreSourceNotFound, "spec.source",
				fmt.Sprintf("backup %q not found", backupName))
			return "", nil, false
		}
		status, _ := backup["status"].(map[string]any)
		if status["phase"] != "Completed" {
			mockError(w, http.StatusConflict, client.CodeRestoreSourceNotReady,
				fmt.Sprintf("backup %q has not completed", backupName))
			return "", nil, false
		}
		source := m.backupSources[backupName]
		version, storage = strOrEmpty(source["version"]), strOrEmpty(source["storage"])
	} else {
		stored["database"] = database
		db, ok := m.databases[database]
		if !ok {
			mockFieldError(w, http.StatusBadRequest, client.CodeRestoreSourceNotFound, "spec.source",
				fmt.Sprintf("database %q not found", database))
			return "", nil, false
		}
		if at, set := stored["pointInTime"].(string); set && (at < mockRecoveryWindowEarliest || at > mockRecoveryWindowLatest) {
			mockFieldError(w, http.StatusBadRequest, client.CodeRestorePointOutsideWindow, "spec.source.pointInTime",
				fmt.Sprintf("%s is outside the restore window of database %q", at, database))
			return "", nil, false
		}
		resources, _ := db["resources"].(map[string]any)
		version, storage = strOrEmpty(db["version"]), strOrEmpty(resources["storage"])
	}
	if requested := strOrEmpty(body["version"]); requested != "" && requested != version {
		mockFieldError(w, http.StatusBadRequest, client.CodeRestoreVersionMismatch, "spec.version",
			fmt.Sprintf("a restore runs the source's major version %s", version))
		return "", nil, false
	}
	resources, _ := body["resources"].(map[string]any)
	requested, okRequested := parseSizeQuantity(strOrEmpty(resources["storage"]))
	needed, okNeeded := parseSizeQuantity(storage)
	if okRequested && okNeeded && requested.Cmp(needed) < 0 {
		mockFieldError(w, http.StatusBadRequest, client.CodeRestoreStorageTooSmall, "spec.resources.storage",
			fmt.Sprintf("a restore needs at least the source's storage, %s", storage))
		return "", nil, false
	}
	return version, stored, true
}
