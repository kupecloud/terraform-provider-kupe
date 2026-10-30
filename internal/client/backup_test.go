package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCreateDatabaseBackup_NameOnTheWire(t *testing.T) {
	tests := []struct {
		name, backup, wantBody string
	}{
		{name: "named", backup: "orders-db-before-migration", wantBody: `{"name":"orders-db-before-migration"}`},
		// Without a name the API chooses one.
		{name: "unnamed", backup: "", wantBody: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.onJSON("POST", "/api/v1/tenants/acme/databases/orders-db/backups", http.StatusCreated, map[string]any{
				"name": "orders-db-20261030-141500", "database": "orders-db", "reason": "Manual",
				"status":          map[string]any{"phase": "Pending", "databaseDeleted": false},
				"resourceVersion": "7", "createdAt": "2026-10-30T14:15:00Z",
			})

			backup, etag, err := mock.client("acme").CreateDatabaseBackup(context.Background(), "orders-db", tt.backup)
			if err != nil {
				t.Fatal(err)
			}
			if got := mock.lastRequest().Body; got != tt.wantBody+"\n" && got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if etag != `"12345"` || backup.Name != "orders-db-20261030-141500" || backup.Status.Phase != "Pending" {
				t.Errorf("got %+v, etag %q", backup, etag)
			}
		})
	}
}

func TestGetBackup(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("GET", "/api/v1/tenants/acme/backups/orders-db-20261030-141500", http.StatusOK, map[string]any{
		"name": "orders-db-20261030-141500", "database": "orders-db", "reason": "Manual",
		"status": map[string]any{
			"phase": "Completed", "completedAt": "2026-10-30T14:17:00Z", "sizeBytes": 1073741824,
			"source":          map[string]any{"name": "orders-db", "version": "18", "storage": "10Gi"},
			"databaseDeleted": true,
			"conditions":      []any{map[string]any{"type": "Ready", "status": "True", "message": "completed"}},
		},
		"resourceVersion": "9", "createdAt": "2026-10-30T14:15:00Z",
	})

	backup, _, err := mock.client("acme").GetBackup(context.Background(), "orders-db-20261030-141500")
	if err != nil {
		t.Fatal(err)
	}
	s := backup.Status
	if backup.Database != "orders-db" || s.Phase != "Completed" || s.CompletedAt != "2026-10-30T14:17:00Z" ||
		s.SizeBytes != 1073741824 || !s.DatabaseDeleted || s.Source == nil ||
		*s.Source != (BackupSource{Name: "orders-db", Version: "18", Storage: "10Gi"}) ||
		ConditionMessage(s.Conditions, ConditionReady) != "completed" {
		t.Errorf("unexpected backup: %+v, status %+v", backup, s)
	}
}

func TestDeleteBackup(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.on("DELETE", "/api/v1/tenants/acme/backups/nightly", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := mock.client("acme").DeleteBackup(context.Background(), "nightly"); err != nil {
		t.Fatal(err)
	}
	if req := mock.lastRequest(); req.Method != "DELETE" || req.Path != "/api/v1/tenants/acme/backups/nightly" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
}

func TestDatabaseSource_OnTheWire(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()
	mock.onJSON("POST", "/api/v1/tenants/acme/databases", http.StatusCreated, map[string]any{
		"name": "orders-copy", "version": "17",
		"source": map[string]any{"database": "orders-db", "pointInTime": "2026-10-30T09:00:00Z"},
	})

	db, _, err := mock.client("acme").CreateDatabase(context.Background(), CreateDatabaseRequest{
		Name:   "orders-copy",
		Source: &DatabaseSource{Database: "orders-db", PointInTime: "2026-10-30T09:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(mock.lastRequest().Body), &sent); err != nil {
		t.Fatal(err)
	}
	// Version is left out so the platform runs the source's major version,
	// and the source carries only the keys set.
	if _, ok := sent["version"]; ok {
		t.Errorf("version sent: %v", sent["version"])
	}
	source, _ := sent["source"].(map[string]any)
	if len(source) != 2 || source["database"] != "orders-db" || source["pointInTime"] != "2026-10-30T09:00:00Z" {
		t.Errorf("source sent = %v", sent["source"])
	}
	if db.Source == nil || *db.Source != (DatabaseSource{Database: "orders-db", PointInTime: "2026-10-30T09:00:00Z"}) {
		t.Errorf("echoed source = %+v", db.Source)
	}

	// A database that was not restored sends and holds no source.
	mock.onJSON("POST", "/api/v1/tenants/acme/databases", http.StatusCreated, map[string]any{"name": "orders-db"})
	db, _, err = mock.client("acme").CreateDatabase(context.Background(), CreateDatabaseRequest{Name: "orders-db"})
	if err != nil {
		t.Fatal(err)
	}
	sent = nil
	if err := json.Unmarshal([]byte(mock.lastRequest().Body), &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["source"]; ok || db.Source != nil {
		t.Errorf("source sent %v, echoed %+v; want neither", sent["source"], db.Source)
	}
}
