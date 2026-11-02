package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestVaultConditionalWriteAndMetadata(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "create only", true: "compare version"}[existing], func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/v1/tenants/acme/vault/apps/config" || r.URL.RawQuery != "" {
					t.Error("wrong path or values query")
				}
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"version":3,"deleted":true,"references":["app"]}`)
					return
				}
				wantMatch, wantNone := "", "*"
				if existing {
					wantMatch, wantNone = `"3"`, ""
				}
				if r.Header.Get("If-Match") != wantMatch || r.Header.Get("If-None-Match") != wantNone {
					t.Error("incorrect CAS headers")
				}
				var body struct {
					Values map[string]string `json:"values"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Values["password"] != "test-secret-sentinel-must-not-appear" {
					t.Error("values not sent")
				}
				_, _ = io.WriteString(w, `{"version":4,"refreshRequested":["app"]}`)
			}))
			defer server.Close()
			c := New(server.URL, "acme", "test-token")
			entry, err := c.GetVaultEntry(context.Background(), "apps/config")
			if err != nil || entry.Version != 3 || !entry.Deleted || len(entry.References) != 1 {
				t.Fatalf("metadata = %#v, %v", entry, err)
			}
			var match *int64
			if existing {
				match = &entry.Version
			}
			result, err := c.PutVaultValues(context.Background(), "apps/config", map[string]string{"password": "test-secret-sentinel-must-not-appear"}, match)
			if err != nil || result.Version != 4 || len(result.RefreshRequested) != 1 || calls.Load() != 2 {
				t.Fatalf("write = %#v, %v; calls %d", result, err, calls.Load())
			}
		})
	}
}

func TestVaultWriteNeverRetries(t *testing.T) {
	for _, fault := range []string{"503", "disconnect", "invalid confirmed response"} {
		t.Run(fault, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				switch fault {
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				case "503":
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"code":"secret_write_unconfirmed","message":"internal server error"}`)
				default:
					_, _ = io.WriteString(w, `{}`)
				}
			}))
			defer server.Close()
			_, err := New(server.URL, "acme", "test-token").PutVaultValues(context.Background(), "apps/config", map[string]string{"key": "test-value"}, nil)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("error %v; PUT count %d, want one", err, calls.Load())
			}
			if fault == "503" && ErrorCode(err) != CodeSecretWriteUnconfirmed {
				t.Errorf("lost typed refusal: %v", err)
			}
		})
	}
}

func TestVaultTypedRefusalsAndDisabledIsNotAbsent(t *testing.T) {
	for _, tt := range []struct {
		status int
		code   string
	}{{412, CodeSecretVersionConflict}, {503, CodeSecretWriteUnconfirmed}, {403, CodeSecretPathReserved}, {503, CodeVaultUnavailable}, {404, CodeVaultNotConfigured}} {
		t.Run(tt.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": tt.code, "message": "refused"})
			}))
			defer server.Close()
			_, err := New(server.URL, "acme", "test-token").PutVaultValues(context.Background(), "apps/config", map[string]string{"key": "test-value"}, nil)
			var typed *APIError
			if !errors.As(err, &typed) || typed.StatusCode != tt.status || ErrorCode(err) != tt.code || IsNotFound(err) {
				t.Fatalf("classification lost: %v", err)
			}
		})
	}
}

func TestRefreshSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/secrets/app/refresh") {
			t.Error("wrong refresh route")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"name":"app","requestedAt":"opaque-token"}`)
	}))
	defer server.Close()
	token, err := New(server.URL, "acme", "test-token").RefreshSecret(context.Background(), "app")
	if err != nil || token != "opaque-token" {
		t.Fatalf("refresh token %q, %v", token, err)
	}
}
