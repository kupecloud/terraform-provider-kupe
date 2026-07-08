package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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

// TestAddUnknownConfigErrors is the LOW-3 regression guard: a provider
// attribute wired to an unknown (not-yet-known) value must produce an
// attribute error rather than silently falling back to the environment.
// Known and null values must pass through untouched (null legitimately
// means "read the env var").
func TestAddUnknownConfigErrors(t *testing.T) {
	known := KupeProviderModel{
		Host:   types.StringValue("https://api.kupe.cloud"),
		Tenant: types.StringNull(),
		APIKey: types.StringValue("kupe_key"),
		Token:  types.StringNull(),
	}

	t.Run("known and null values produce no error", func(t *testing.T) {
		var diags diag.Diagnostics
		addUnknownConfigErrors(known, &diags)
		if diags.HasError() {
			t.Fatalf("expected no error diagnostics, got %v", diags)
		}
	})

	cases := []struct {
		name  string
		model KupeProviderModel
	}{
		{"unknown host", func() KupeProviderModel { m := known; m.Host = types.StringUnknown(); return m }()},
		{"unknown tenant", func() KupeProviderModel { m := known; m.Tenant = types.StringUnknown(); return m }()},
		{"unknown api_key", func() KupeProviderModel { m := known; m.APIKey = types.StringUnknown(); return m }()},
		{"unknown token", func() KupeProviderModel { m := known; m.Token = types.StringUnknown(); return m }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			addUnknownConfigErrors(tc.model, &diags)
			if !diags.HasError() {
				t.Fatalf("expected an error diagnostic for %s, got none", tc.name)
			}
		})
	}
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
