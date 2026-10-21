package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestGetDatabase(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/tenants/acme/databases/orders-db", http.StatusOK, map[string]any{
		"name": "orders-db", "engine": "postgres", "version": "18",
		"resources":          map[string]any{"cpu": "500m", "memory": "1Gi", "storage": "10Gi"},
		"deletionProtection": true, "generation": 3,
		"status": map[string]any{
			"phase": "Running", "observedGeneration": 3, "currentVersion": "18.0",
			"conditions": []any{
				map[string]any{"type": "Provisioned", "status": "True"},
				map[string]any{"type": "Ready", "status": "False", "reason": "NotHealthy", "message": "applying changes"},
			},
		},
	})

	db, etag, err := mock.client("acme").GetDatabase(context.Background(), "orders-db")
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"12345"` {
		t.Errorf("etag = %q, want %q", etag, `"12345"`)
	}
	if db.Resources.Storage != "10Gi" || !db.DeletionProtection || db.Generation != 3 {
		t.Errorf("unexpected database: %+v", db)
	}
	if db.Status.CurrentVersion != "18.0" || db.Status.ObservedGeneration != 3 {
		t.Errorf("unexpected status: %+v", db.Status)
	}
	if db.Status.Ready() || ConditionMessage(db.Status.Conditions, ConditionReady) != "applying changes" {
		t.Errorf("Ready() = %v, message %q; want false, \"applying changes\"",
			db.Status.Ready(), ConditionMessage(db.Status.Conditions, ConditionReady))
	}
}

func TestConditionTrue(t *testing.T) {
	conditions := []Condition{{Type: "Provisioned", Status: "True"}, {Type: "Ready", Status: "False"}}
	if !ConditionTrue(conditions, "Provisioned") || ConditionTrue(conditions, "Ready") || ConditionTrue(conditions, "Missing") {
		t.Errorf("ConditionTrue gave the wrong answer for %+v", conditions)
	}
	var status *DatabaseStatus
	if status.Ready() {
		t.Error("a database with no status reads Ready")
	}
}

func TestCreateDatabase_DeletionProtectionOnTheWire(t *testing.T) {
	protectionOff := false
	tests := []struct {
		name    string
		value   *bool
		wantKey bool
	}{
		{name: "omitted when unset so the API default applies", value: nil, wantKey: false},
		{name: "explicit false is sent", value: &protectionOff, wantKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.onJSON("POST", "/api/v1/tenants/acme/databases", http.StatusCreated, map[string]any{"name": "orders-db"})

			_, _, err := mock.client("acme").CreateDatabase(context.Background(), CreateDatabaseRequest{
				Name:               "orders-db",
				Resources:          &ResourceQuantities{CPU: "500m", Memory: "1Gi", Storage: "10Gi"},
				DeletionProtection: tt.value,
			})
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(mock.lastRequest().Body), &body); err != nil {
				t.Fatal(err)
			}
			got, present := body["deletionProtection"]
			if present != tt.wantKey {
				t.Fatalf("deletionProtection present = %v, want %v (body %v)", present, tt.wantKey, body)
			}
			if present && got != false {
				t.Errorf("deletionProtection = %v, want false", got)
			}
		})
	}
}

func TestUpdateDatabase_SendsIfMatchAndOnlySetFields(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("PATCH", "/api/v1/tenants/acme/databases/orders-db", http.StatusOK, map[string]any{"name": "orders-db"})

	ha := true
	_, _, err := mock.client("acme").UpdateDatabase(context.Background(), "orders-db", `"41"`, PatchDatabaseRequest{
		HighAvailability: &ha,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := mock.lastRequest()
	if got := req.Header.Get("If-Match"); got != `"41"` {
		t.Errorf("If-Match = %q, want %q", got, `"41"`)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["highAvailability"] != true {
		t.Errorf("PATCH body = %v, want only highAvailability=true", body)
	}
}

func TestDeleteDatabase_DeletionProtected(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("DELETE", "/api/v1/tenants/acme/databases/orders-db", http.StatusConflict, map[string]any{
		"error": "database has deletionProtection enabled", "code": CodeDeletionProtected,
		"severity": "error", "message": "database has deletionProtection enabled",
	})

	err := mock.client("acme").DeleteDatabase(context.Background(), "orders-db")
	if !IsDeletionProtected(err) {
		t.Fatalf("IsDeletionProtected(%v) = false, want true", err)
	}
}

func TestIsDeletionProtected(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "409 with the code", err: &APIError{StatusCode: http.StatusConflict, Code: CodeDeletionProtected}, want: true},
		{name: "409 without the code", err: &APIError{StatusCode: http.StatusConflict}, want: false},
		{name: "other status with the code", err: &APIError{StatusCode: http.StatusBadRequest, Code: CodeDeletionProtected}, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDeletionProtected(tt.err); got != tt.want {
				t.Errorf("IsDeletionProtected() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsDatabasesUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "404 with the code", err: &APIError{StatusCode: http.StatusNotFound, Code: CodeDatabasesUnavailable}, want: true},
		{name: "plain 404", err: &APIError{StatusCode: http.StatusNotFound}, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDatabasesUnavailable(tt.err); got != tt.want {
				t.Errorf("IsDatabasesUnavailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDatabaseBindingLifecycle(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	const itemPath = "/api/v1/tenants/acme/databases/orders-db/bindings/orders-db-prod-readwrite"
	mock.onJSON("POST", "/api/v1/tenants/acme/databases/orders-db/bindings", http.StatusCreated, map[string]any{
		"name": "orders-db-prod-readwrite", "database": "orders-db", "cluster": "prod",
		"namespaces": []string{"api", "jobs"}, "role": "readwrite",
	})
	mock.onJSON("GET", itemPath, http.StatusOK, map[string]any{
		"name": "orders-db-prod-readwrite", "database": "orders-db", "namespaces": []string{"api", "jobs"},
		"status": map[string]any{
			"phase": "Ready", "secretName": "orders-db-readwrite",
			"namespaces": []map[string]any{
				{"name": "api", "host": "orders-db.api.svc.cluster.local", "phase": "Ready", "message": ""},
				{"name": "jobs", "host": "orders-db.jobs.svc.cluster.local", "phase": "Ready", "message": ""},
			},
		},
	})
	mock.on("DELETE", itemPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := mock.client("acme")
	ctx := context.Background()

	created, etag, err := c.CreateDatabaseBinding(ctx, "orders-db", CreateDatabaseBindingRequest{
		Name: "orders-db-prod-readwrite", Cluster: "prod", Namespaces: []string{"api", "jobs"}, Role: "readwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Cluster != "prod" || etag != `"12345"` {
		t.Errorf("created cluster = %q, ETag %q; want prod and the response's ETag", created.Cluster, etag)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(mock.lastRequest().Body), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["secretName"]; present {
		t.Errorf("secretName must be omitted when unset so the API default applies, got body %v", body)
	}
	if _, present := body["namespace"]; present {
		t.Errorf("the API refuses the old namespace field, got body %v", body)
	}
	if fmt.Sprint(body["namespaces"]) != "[api jobs]" {
		t.Errorf("namespaces = %v, want [api jobs]", body["namespaces"])
	}

	got, etag, err := c.GetDatabaseBinding(ctx, "orders-db", "orders-db-prod-readwrite")
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"12345"` {
		t.Errorf("GET ETag = %q, want the response's ETag", etag)
	}
	if len(got.Status.Namespaces) != 2 || got.Status.Namespaces[1].Host != "orders-db.jobs.svc.cluster.local" ||
		got.Status.SecretName != "orders-db-readwrite" {
		t.Errorf("unexpected status: %+v", got.Status)
	}

	if err := c.DeleteDatabaseBinding(ctx, "orders-db", "orders-db-prod-readwrite"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDatabaseBinding(ctx, "orders-db", "missing"); !IsNotFound(err) {
		t.Errorf("delete of a missing binding: IsNotFound(%v) = false, want true", err)
	}
}

// TestUpdateDatabaseBinding: the PATCH carries If-Match and only the
// complete new namespace list.
func TestUpdateDatabaseBinding(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("PATCH", "/api/v1/tenants/acme/databases/orders-db/bindings/orders-db-prod-readwrite", http.StatusOK,
		map[string]any{"name": "orders-db-prod-readwrite", "namespaces": []string{"api", "reports"}})

	got, etag, err := mock.client("acme").UpdateDatabaseBinding(context.Background(), "orders-db",
		"orders-db-prod-readwrite", `"41"`, PatchDatabaseBindingRequest{Namespaces: []string{"api", "reports"}})
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"12345"` || fmt.Sprint(got.Namespaces) != "[api reports]" {
		t.Errorf("got namespaces %v, ETag %q; want the response's", got.Namespaces, etag)
	}
	req := mock.lastRequest()
	if got := req.Header.Get("If-Match"); got != `"41"` {
		t.Errorf("If-Match = %q, want %q", got, `"41"`)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || fmt.Sprint(body["namespaces"]) != "[api reports]" {
		t.Errorf("PATCH body = %v, want only namespaces=[api reports]", body)
	}
}

// TestGetDatabase_Backup decodes the backup settings and status: set, a
// retention never set, and a database that never set them.
func TestGetDatabase_Backup(t *testing.T) {
	tests := []struct {
		name          string
		backup        any
		status        map[string]any
		wantRetention *int32
		wantSchedule  *string
		wantWindow    *RecoveryWindow
		wantLast      string
		wantSize      int64
	}{
		{
			name:   "settings and status reported",
			backup: map[string]any{"retention": 14, "schedule": "DailyThenWeekly"},
			status: map[string]any{
				"lastBackupAt":    "2026-10-21T03:17:00Z",
				"recoveryWindow":  map[string]any{"earliest": "2026-10-07T03:17:00Z", "latest": "2026-10-21T09:00:00Z"},
				"backupSizeBytes": 5368709120,
			},
			wantRetention: int32Ptr(14), wantSchedule: stringPtr("DailyThenWeekly"),
			wantWindow: &RecoveryWindow{Earliest: "2026-10-07T03:17:00Z", Latest: "2026-10-21T09:00:00Z"},
			wantLast:   "2026-10-21T03:17:00Z", wantSize: 5368709120,
		},
		{
			name:         "a retention never set is absent",
			backup:       map[string]any{"schedule": "Daily"},
			status:       map[string]any{"lastBackupAt": "", "recoveryWindow": nil, "backupSizeBytes": 0},
			wantSchedule: stringPtr("Daily"),
		},
		{
			name:   "settings never set are null",
			backup: nil,
			status: map[string]any{"lastBackupAt": "", "recoveryWindow": nil, "backupSizeBytes": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.onJSON("GET", "/api/v1/tenants/acme/databases/orders-db", http.StatusOK, map[string]any{
				"name": "orders-db", "backup": tt.backup, "status": tt.status,
			})
			db, _, err := mock.client("acme").GetDatabase(context.Background(), "orders-db")
			if err != nil {
				t.Fatal(err)
			}
			if tt.backup == nil {
				if db.Backup != nil {
					t.Fatalf("Backup = %+v, want nil", db.Backup)
				}
			} else {
				if db.Backup == nil {
					t.Fatal("Backup = nil, want the settings")
				}
				if !equalPtr(db.Backup.Retention, tt.wantRetention) || !equalPtr(db.Backup.Schedule, tt.wantSchedule) {
					t.Errorf("Backup = %s, want retention %s schedule %s",
						describeBackup(db.Backup), describePtr(tt.wantRetention), describePtr(tt.wantSchedule))
				}
			}
			if db.Status.LastBackupAt != tt.wantLast || db.Status.BackupSizeBytes != tt.wantSize {
				t.Errorf("lastBackupAt %q, backupSizeBytes %d; want %q, %d",
					db.Status.LastBackupAt, db.Status.BackupSizeBytes, tt.wantLast, tt.wantSize)
			}
			if !equalPtr(db.Status.RecoveryWindow, tt.wantWindow) {
				t.Errorf("RecoveryWindow = %+v, want %+v", db.Status.RecoveryWindow, tt.wantWindow)
			}
		})
	}
}

// TestDatabaseRequests_BackupOnTheWire: backup is sent only when set, and
// then with only the keys set.
func TestDatabaseRequests_BackupOnTheWire(t *testing.T) {
	tests := []struct {
		name   string
		backup *DatabaseBackup
		want   string // the body's backup, "" when absent
	}{
		{name: "omitted when unset", backup: nil, want: ""},
		{name: "retention only", backup: &DatabaseBackup{Retention: int32Ptr(14)}, want: `{"retention":14}`},
		{name: "schedule only", backup: &DatabaseBackup{Schedule: stringPtr("DailyThenWeekly")}, want: `{"schedule":"DailyThenWeekly"}`},
		{
			name:   "both",
			backup: &DatabaseBackup{Retention: int32Ptr(21), Schedule: stringPtr("Daily")},
			want:   `{"retention":21,"schedule":"Daily"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.onJSON("POST", "/api/v1/tenants/acme/databases", http.StatusCreated, map[string]any{"name": "orders-db"})
			mock.onJSON("PATCH", "/api/v1/tenants/acme/databases/orders-db", http.StatusOK, map[string]any{"name": "orders-db"})
			c := mock.client("acme")
			ctx := context.Background()

			if _, _, err := c.CreateDatabase(ctx, CreateDatabaseRequest{Name: "orders-db", Backup: tt.backup}); err != nil {
				t.Fatal(err)
			}
			if got := backupInBody(t, mock.lastRequest().Body); got != tt.want {
				t.Errorf("POST backup = %q, want %q", got, tt.want)
			}
			ha := true
			if _, _, err := c.UpdateDatabase(ctx, "orders-db", `"1"`, PatchDatabaseRequest{HighAvailability: &ha, Backup: tt.backup}); err != nil {
				t.Fatal(err)
			}
			if got := backupInBody(t, mock.lastRequest().Body); got != tt.want {
				t.Errorf("PATCH backup = %q, want %q", got, tt.want)
			}
		})
	}
}

// backupInBody is the raw backup object in a request body, "" when absent.
func backupInBody(t *testing.T, body string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	return string(fields["backup"])
}

func int32Ptr(v int32) *int32    { return &v }
func stringPtr(v string) *string { return &v }

// equalPtr reports whether two pointers are both nil or point at equal
// values.
func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func describePtr[T any](p *T) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprint(*p)
}

func describeBackup(b *DatabaseBackup) string {
	return fmt.Sprintf("{retention %s schedule %s}", describePtr(b.Retention), describePtr(b.Schedule))
}
