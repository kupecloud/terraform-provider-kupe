package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestStringValueOrEnv(t *testing.T) {
	t.Run("returns config value when set", func(t *testing.T) {
		v := stringValueOrEnv(types.StringValue("config-val"), "TEST_ENV")
		if v != "config-val" {
			t.Errorf("expected config-val, got %q", v)
		}
	})

	t.Run("returns env when config null", func(t *testing.T) {
		t.Setenv("TEST_PROVIDER_ENV", "env-val")
		v := stringValueOrEnv(types.StringNull(), "TEST_PROVIDER_ENV")
		if v != "env-val" {
			t.Errorf("expected env-val, got %q", v)
		}
	})

	t.Run("returns empty when neither set", func(t *testing.T) {
		t.Setenv("TEST_PROVIDER_MISSING", "")
		v := stringValueOrEnv(types.StringNull(), "TEST_PROVIDER_MISSING")
		if v != "" {
			t.Errorf("expected empty, got %q", v)
		}
	})

	t.Run("returns env when config unknown", func(t *testing.T) {
		t.Setenv("TEST_PROVIDER_UNK", "env-val")
		v := stringValueOrEnv(types.StringUnknown(), "TEST_PROVIDER_UNK")
		if v != "env-val" {
			t.Errorf("expected env-val, got %q", v)
		}
	})
}

func TestNew(t *testing.T) {
	factory := New("1.0.0")
	if factory == nil {
		t.Fatal("expected non-nil factory")
	}
	p := factory()
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestSelectAuthToken(t *testing.T) {
	t.Run("accepts api key", func(t *testing.T) {
		token, err := selectAuthToken("api-key", "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if token != "api-key" {
			t.Fatalf("expected api-key, got %q", token)
		}
	})

	t.Run("accepts oidc token", func(t *testing.T) {
		token, err := selectAuthToken("", "oidc-token")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if token != "oidc-token" {
			t.Fatalf("expected oidc-token, got %q", token)
		}
	})

	t.Run("rejects both auth methods", func(t *testing.T) {
		_, err := selectAuthToken("api-key", "oidc-token")
		if err == nil {
			t.Fatal("expected error when both auth methods are set")
		}
	})

	t.Run("rejects missing auth", func(t *testing.T) {
		_, err := selectAuthToken("", "")
		if err == nil {
			t.Fatal("expected error when authentication is missing")
		}
	})
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "accepts https host",
			input: "https://api.kupe.cloud",
			want:  "https://api.kupe.cloud",
		},
		{
			name:  "trims trailing slash",
			input: "https://api.kupe.cloud/",
			want:  "https://api.kupe.cloud",
		},
		{
			name:  "accepts localhost over http",
			input: "http://localhost:8080",
			want:  "http://localhost:8080",
		},
		{
			name:  "accepts loopback ip over http",
			input: "http://127.0.0.1:8080",
			want:  "http://127.0.0.1:8080",
		},
		{
			name:    "rejects missing scheme",
			input:   "api.kupe.cloud",
			wantErr: true,
		},
		{
			name:    "rejects non local http",
			input:   "http://api.kupe.cloud",
			wantErr: true,
		},
		{
			name:    "rejects path",
			input:   "https://api.kupe.cloud/v1",
			wantErr: true,
		},
		{
			name:    "rejects query",
			input:   "https://api.kupe.cloud?debug=true",
			wantErr: true,
		},
		{
			name:    "rejects fragments",
			input:   "https://api.kupe.cloud#fragment",
			wantErr: true,
		},
		{
			name:    "rejects unsupported scheme",
			input:   "ftp://api.kupe.cloud",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeHost(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
