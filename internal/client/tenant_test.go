package client

import (
	"context"
	"net/http"
	"testing"
)

func TestGetTenant(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/tenants/acme", http.StatusOK, Tenant{
		Name:         "acme",
		DisplayName:  "Acme Corp",
		ContactEmail: "admin@acme.com",
		Plan:         "pro",
	})

	c := mock.client("acme")
	tenant, etag, err := c.GetTenant(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Name != "acme" {
		t.Errorf("expected acme, got %q", tenant.Name)
	}
	if tenant.Plan != "pro" {
		t.Errorf("expected pro, got %q", tenant.Plan)
	}
	if etag == "" {
		t.Error("expected non-empty etag")
	}
}

func TestGetPlan(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.onJSON("GET", "/api/v1/plans/starter", http.StatusOK, Plan{
		Name:        "starter",
		DisplayName: "Starter",
		PlatformFee: "29.00",
		MaxClusters: 3,
	})

	c := mock.client("acme")
	plan, err := c.GetPlan(context.Background(), "starter")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "starter" {
		t.Errorf("expected starter, got %q", plan.Name)
	}
	if plan.MaxClusters != 3 {
		t.Errorf("expected 3, got %d", plan.MaxClusters)
	}
}

func TestGetPlan_DatabaseLimit(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want *int64
	}{
		{name: "limited", body: map[string]any{"name": "pro", "maxClusters": 5, "maxDatabases": 5}, want: new(int64(5))},
		{name: "unlimited", body: map[string]any{"name": "enterprise", "maxClusters": 0, "maxDatabases": 0}, want: new(int64(0))},
		{name: "not reported", body: map[string]any{"name": "pro", "maxClusters": 5}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockAPI()
			defer mock.close()
			mock.onJSON("GET", "/api/v1/plans/pro", http.StatusOK, tt.body)

			plan, err := mock.client("acme").GetPlan(context.Background(), "pro")
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.want == nil && plan.MaxDatabases != nil:
				t.Errorf("MaxDatabases = %d, want nil", *plan.MaxDatabases)
			case tt.want != nil && plan.MaxDatabases == nil:
				t.Errorf("MaxDatabases = nil, want %d", *tt.want)
			case tt.want != nil && *plan.MaxDatabases != *tt.want:
				t.Errorf("MaxDatabases = %d, want %d", *plan.MaxDatabases, *tt.want)
			}
		})
	}
}

func TestGetPlan_NotFound(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	c := mock.client("acme")
	_, err := c.GetPlan(context.Background(), "nonexistent")
	if !IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}
}
