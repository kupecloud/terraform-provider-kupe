package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func mustEncode(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

// --- Receivers ---

func TestGetAlertmanagerReceiver(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("GET", "/api/v1/tenants/acme/alertmanager/receivers/slack", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv1"`)
		mustEncode(w, map[string]any{
			"name":          "slack",
			"slack_configs": []any{map[string]any{"channel": "#alerts"}},
		})
	})

	c := mock.client("acme")
	recv, etag, err := c.GetAlertmanagerReceiver(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	if recv["name"] != "slack" {
		t.Errorf("expected name=slack, got %v", recv["name"])
	}
	if etag != `"rv1"` {
		t.Errorf("expected ETag, got %q", etag)
	}
}

func TestGetAlertmanagerReceiver_NotFound(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	c := mock.client("acme")
	_, _, err := c.GetAlertmanagerReceiver(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}
}

func TestPutAlertmanagerReceiver(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PUT", "/api/v1/tenants/acme/alertmanager/receivers/slack", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != `"rv1"` {
			t.Errorf("expected If-Match header, got %q", r.Header.Get("If-Match"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv2"`)
		mustEncode(w, map[string]any{
			"name":          "slack",
			"slack_configs": []any{},
		})
	})

	c := mock.client("acme")
	recv := AlertmanagerReceiver{"name": "slack", "slack_configs": []any{}}
	out, etag, err := c.PutAlertmanagerReceiver(context.Background(), "slack", `"rv1"`, recv)
	if err != nil {
		t.Fatal(err)
	}
	if out["name"] != "slack" {
		t.Errorf("expected name=slack, got %v", out["name"])
	}
	if etag != `"rv2"` {
		t.Errorf("expected new ETag, got %q", etag)
	}
}

func TestPutAlertmanagerReceiver_Create(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PUT", "/api/v1/tenants/acme/alertmanager/receivers/pagerduty", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != "" {
			t.Errorf("expected no If-Match on create, got %q", r.Header.Get("If-Match"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv1"`)
		mustEncode(w, map[string]any{"name": "pagerduty"})
	})

	c := mock.client("acme")
	recv := AlertmanagerReceiver{"name": "pagerduty"}
	_, etag, err := c.PutAlertmanagerReceiver(context.Background(), "pagerduty", "", recv)
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"rv1"` {
		t.Errorf("expected ETag, got %q", etag)
	}
}

func TestDeleteAlertmanagerReceiver(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("DELETE", "/api/v1/tenants/acme/alertmanager/receivers/slack", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	c := mock.client("acme")
	if err := c.DeleteAlertmanagerReceiver(context.Background(), "slack"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteAlertmanagerReceiver_NotFound(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	c := mock.client("acme")
	err := c.DeleteAlertmanagerReceiver(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}
}

// --- Routes ---

func TestGetAlertmanagerRoutes(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("GET", "/api/v1/tenants/acme/alertmanager/routes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv1"`)
		mustEncode(w, map[string]any{
			"items": []map[string]any{
				{"receiver": "slack", "matchers": []string{`severity="critical"`}},
				{"receiver": "pagerduty"},
			},
		})
	})

	c := mock.client("acme")
	routes, etag, err := c.GetAlertmanagerRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].Receiver != "slack" {
		t.Errorf("expected receiver=slack, got %q", routes[0].Receiver)
	}
	if etag != `"rv1"` {
		t.Errorf("expected ETag, got %q", etag)
	}
}

func TestPutAlertmanagerRoutes(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PUT", "/api/v1/tenants/acme/alertmanager/routes", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != `"rv1"` {
			t.Errorf("expected If-Match header, got %q", r.Header.Get("If-Match"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv2"`)
		mustEncode(w, map[string]any{
			"items": []map[string]any{
				{"receiver": "slack", "matchers": []string{`severity="warning"`}},
			},
		})
	})

	c := mock.client("acme")
	routes := []*AlertmanagerRoute{
		{Receiver: "slack", Matchers: []string{`severity="warning"`}},
	}
	out, etag, err := c.PutAlertmanagerRoutes(context.Background(), `"rv1"`, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 route, got %d", len(out))
	}
	if out[0].Receiver != "slack" {
		t.Errorf("expected receiver=slack, got %q", out[0].Receiver)
	}
	if etag != `"rv2"` {
		t.Errorf("expected new ETag, got %q", etag)
	}
}

func TestPutAlertmanagerRoutes_Empty(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PUT", "/api/v1/tenants/acme/alertmanager/routes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv2"`)
		mustEncode(w, map[string]any{"items": []any{}})
	})

	c := mock.client("acme")
	out, _, err := c.PutAlertmanagerRoutes(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty routes, got %d", len(out))
	}
}

// --- Global ---

func TestGetAlertmanagerGlobal(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("GET", "/api/v1/tenants/acme/alertmanager/global", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv1"`)
		mustEncode(w, map[string]any{
			"smtp_from":       "alerts@example.com",
			"resolve_timeout": "5m",
		})
	})

	c := mock.client("acme")
	g, etag, err := c.GetAlertmanagerGlobal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g["smtp_from"] != "alerts@example.com" {
		t.Errorf("expected smtp_from, got %v", g["smtp_from"])
	}
	if etag != `"rv1"` {
		t.Errorf("expected ETag, got %q", etag)
	}
}

func TestPutAlertmanagerGlobal(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PUT", "/api/v1/tenants/acme/alertmanager/global", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != `"rv1"` {
			t.Errorf("expected If-Match header, got %q", r.Header.Get("If-Match"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv2"`)
		mustEncode(w, map[string]any{"smtp_from": "ops@example.com"})
	})

	c := mock.client("acme")
	g := AlertmanagerGlobal{"smtp_from": "ops@example.com"}
	out, etag, err := c.PutAlertmanagerGlobal(context.Background(), `"rv1"`, g)
	if err != nil {
		t.Fatal(err)
	}
	if out["smtp_from"] != "ops@example.com" {
		t.Errorf("expected smtp_from, got %v", out["smtp_from"])
	}
	if etag != `"rv2"` {
		t.Errorf("expected new ETag, got %q", etag)
	}
}

func TestPutAlertmanagerGlobal_Clear(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("PUT", "/api/v1/tenants/acme/alertmanager/global", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"rv2"`)
		mustEncode(w, map[string]any{})
	})

	c := mock.client("acme")
	out, _, err := c.PutAlertmanagerGlobal(context.Background(), "", AlertmanagerGlobal{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty global, got %v", out)
	}
}
