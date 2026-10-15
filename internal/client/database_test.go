package client

import (
	"context"
	"encoding/json"
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
	const itemPath = "/api/v1/tenants/acme/databases/orders-db/bindings/orders-db-prod-api-app"
	mock.onJSON("POST", "/api/v1/tenants/acme/databases/orders-db/bindings", http.StatusCreated, map[string]any{
		"name": "orders-db-prod-api-app", "database": "orders-db", "cluster": "prod", "namespace": "api", "role": "app",
	})
	mock.onJSON("GET", itemPath, http.StatusOK, map[string]any{
		"name": "orders-db-prod-api-app", "database": "orders-db",
		"status": map[string]any{"phase": "Ready", "host": "orders-db.api.svc.cluster.local", "secretName": "orders-db-app"},
	})
	mock.on("DELETE", itemPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := mock.client("acme")
	ctx := context.Background()

	created, err := c.CreateDatabaseBinding(ctx, "orders-db", CreateDatabaseBindingRequest{
		Name: "orders-db-prod-api-app", Cluster: "prod", Namespace: "api", Role: "app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Cluster != "prod" {
		t.Errorf("created cluster = %q, want prod", created.Cluster)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(mock.lastRequest().Body), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["secretName"]; present {
		t.Errorf("secretName must be omitted when unset so the API default applies, got body %v", body)
	}

	got, err := c.GetDatabaseBinding(ctx, "orders-db", "orders-db-prod-api-app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Host != "orders-db.api.svc.cluster.local" || got.Status.SecretName != "orders-db-app" {
		t.Errorf("unexpected status: %+v", got.Status)
	}

	if err := c.DeleteDatabaseBinding(ctx, "orders-db", "orders-db-prod-api-app"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDatabaseBinding(ctx, "orders-db", "missing"); !IsNotFound(err) {
		t.Errorf("delete of a missing binding: IsNotFound(%v) = false, want true", err)
	}
}
