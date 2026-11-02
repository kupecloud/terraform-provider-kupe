package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestNotPlatformNamespace pins the reserved-namespace rules the provider
// copies from kupe-control-operator domain.IsPlatformNamespace: the prefix
// kube- and the name argocd. A change to either side is a deliberate edit to
// both this test and the validator.
func TestNotPlatformNamespace(t *testing.T) {
	tests := []struct {
		namespace string
		reserved  bool
	}{
		{"kube-system", true},
		{"kube-public", true},
		{"kube-", true},
		{"argocd", true},
		{"default", false},
		{"kube", false},
		{"kubeflow", false},
		{"argocd-apps", false},
		{"my-argocd", false},
		{"ci", false},
	}
	for _, tt := range tests {
		t.Run(tt.namespace, func(t *testing.T) {
			var resp validator.StringResponse
			notPlatformNamespace{}.ValidateString(context.Background(), validator.StringRequest{
				Path:        path.Root("push_namespaces"),
				ConfigValue: types.StringValue(tt.namespace),
			}, &resp)
			if got := resp.Diagnostics.HasError(); got != tt.reserved {
				t.Errorf("namespace %q refused = %v, want %v", tt.namespace, got, tt.reserved)
			}
		})
	}
}

func TestConfigMapKey(t *testing.T) {
	for key, want := range map[string]bool{
		"password": true, "CA.crt": true, "_": true, "-": true, ".hidden": true, "a..b": true,
		strings.Repeat("x", 253): true, strings.Repeat("x", 254): false,
		"": false, ".": false, "..": false, "..hidden": false, "a/b": false, "a b": false,
		"clé": false, "key\n": false,
	} {
		t.Run(key, func(t *testing.T) {
			var resp validator.StringResponse
			configMapKey{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("keys"), ConfigValue: types.StringValue(key)}, &resp)
			if resp.Diagnostics.HasError() == want {
				t.Errorf("key accepted = %v, want %v", !resp.Diagnostics.HasError(), want)
			}
		})
	}
	for _, value := range []types.String{types.StringNull(), types.StringUnknown()} {
		var resp validator.StringResponse
		configMapKey{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("keys"), ConfigValue: value}, &resp)
		if resp.Diagnostics.HasError() != value.IsNull() {
			t.Error("a null key must be refused; an unknown key must wait for validation")
		}
	}
}

func TestRegistryStorageLimitPattern(t *testing.T) {
	for limit, want := range map[string]bool{
		"10Gi": true, "512Mi": true, "1Ti": true, "1.5Gi": true,
		"10G": false, "512Ki": false, "1Pi": false, "1024": false, "Gi": false, "-1Gi": false, "10gi": false,
	} {
		if got := registryStorageLimitPattern.MatchString(limit); got != want {
			t.Errorf("registryStorageLimitPattern(%q) = %v, want %v", limit, got, want)
		}
	}
}
