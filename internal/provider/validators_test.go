package provider

import (
	"context"
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
