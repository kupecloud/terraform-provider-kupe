package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestStructuredErrorEnvelope verifies the canonical envelope decodes onto
// APIError.Code/Severity/Field and that Message picks the structured
// `message` field (preferring it over the duplicated `error`).
func TestStructuredErrorEnvelope(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("GET", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":"HA_DISABLE_UNSUPPORTED","severity":"error","message":"Disabling HA is not supported in v1.","field":"spec.highAvailability","error":"Disabling HA is not supported in v1."}`)
	})

	c := mock.client("acme")
	_, _, err := c.GetCluster(context.Background(), "prod")
	if err == nil {
		t.Fatal("want error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T", err)
	}
	if apiErr.Code != "HA_DISABLE_UNSUPPORTED" {
		t.Errorf("Code = %q; want HA_DISABLE_UNSUPPORTED", apiErr.Code)
	}
	if apiErr.Severity != "error" {
		t.Errorf("Severity = %q", apiErr.Severity)
	}
	if apiErr.Field != "spec.highAvailability" {
		t.Errorf("Field = %q", apiErr.Field)
	}
	if apiErr.Message != "Disabling HA is not supported in v1." {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

// TestLegacyErrorEnvelope confirms backward compatibility: a plain
// {"error":"..."} 4xx still populates Message and leaves Code/Field empty.
func TestLegacyErrorEnvelope(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("GET", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"cluster not found"}`)
	})

	c := mock.client("acme")
	_, _, err := c.GetCluster(context.Background(), "prod")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.Message != "cluster not found" {
		t.Errorf("Message = %q; want cluster not found", apiErr.Message)
	}
	if apiErr.Code != "" || apiErr.Field != "" || apiErr.Severity != "" {
		t.Errorf("structured fields should be empty: %+v", apiErr)
	}
}

// TestGarbageBodyFallsBackToRaw covers the not-valid-JSON branch — Message
// keeps the raw body so users see something instead of "".
func TestGarbageBodyFallsBackToRaw(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("GET", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "<html>upstream timeout</html>")
	})

	c := mock.client("acme")
	_, _, err := c.GetCluster(context.Background(), "prod")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.Message != "<html>upstream timeout</html>" {
		t.Errorf("Message = %q; want raw body", apiErr.Message)
	}
}

// TestUnknownJSONShapeFallsBackToRaw covers "valid JSON but neither shape"
// — Message should still carry the raw body string.
func TestUnknownJSONShapeFallsBackToRaw(t *testing.T) {
	body := `{"unexpected":"shape"}`
	mock := newMockAPI()
	defer mock.close()

	mock.on("GET", "/api/v1/tenants/acme/clusters/prod", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, body)
	})

	c := mock.client("acme")
	_, _, err := c.GetCluster(context.Background(), "prod")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.Message != body {
		t.Errorf("Message = %q; want raw body %q", apiErr.Message, body)
	}
}

// TestClusterWarningsUnmarshal verifies POST /clusters responses with a
// populated warnings array decode onto the new Cluster.Warnings field.
func TestClusterWarningsUnmarshal(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("POST", "/api/v1/tenants/acme/clusters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1"`)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{
			"name":"prod",
			"displayName":"Prod",
			"type":"shared",
			"version":"1.33",
			"resourceVersion":"1",
			"createdAt":"2026-06-05T00:00:00Z",
			"warnings":[
				{"code":"HA_K8S_VERSION_RETIRING","severity":"warning","message":"K8s 1.33 is approaching EOL.","field":"spec.highAvailability"}
			]
		}`)
	})

	c := mock.client("acme")
	cluster, _, err := c.CreateCluster(context.Background(), CreateClusterRequest{Name: "prod", DisplayName: "Prod", Type: "shared"})
	if err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if len(cluster.Warnings) != 1 {
		t.Fatalf("Warnings len = %d; want 1", len(cluster.Warnings))
	}
	w := cluster.Warnings[0]
	if w.Code != "HA_K8S_VERSION_RETIRING" || w.Severity != "warning" || w.Field != "spec.highAvailability" {
		t.Errorf("unexpected warning: %+v", w)
	}
}

// TestClusterWarningsEmpty covers the always-present empty array case so
// future kupe-api callers can rely on `[]` decoding cleanly.
func TestClusterWarningsEmpty(t *testing.T) {
	mock := newMockAPI()
	defer mock.close()

	mock.on("POST", "/api/v1/tenants/acme/clusters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1"`)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"name":"prod","displayName":"Prod","type":"shared","resourceVersion":"1","createdAt":"2026-06-05T00:00:00Z","warnings":[]}`)
	})

	c := mock.client("acme")
	cluster, _, err := c.CreateCluster(context.Background(), CreateClusterRequest{Name: "prod", DisplayName: "Prod", Type: "shared"})
	if err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if len(cluster.Warnings) != 0 {
		t.Errorf("Warnings = %+v; want empty", cluster.Warnings)
	}
}

// TestWarningRoundTrip guards JSON tags from accidental rename.
func TestWarningRoundTrip(t *testing.T) {
	in := Warning{Code: "X", Severity: "warning", Message: "m", Field: "spec.f"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Warning
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch: %+v vs %+v", in, out)
	}
}
