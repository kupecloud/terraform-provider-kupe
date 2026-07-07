package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestRoutesJSONSemanticEquals covers MEDIUM-4: routes_json equality must
// ignore JSON zero-values (false, "", null, [], {}) that kupe-api's omitempty
// echo drops, while still surfacing genuine content changes.
func TestRoutesJSONSemanticEquals(t *testing.T) {
	tests := []struct {
		name  string
		a     string
		b     string
		equal bool
	}{
		{
			name:  "explicit continue:false equals stripped echo",
			a:     `[{"receiver":"slack","continue":false}]`,
			b:     `[{"receiver":"slack"}]`,
			equal: true,
		},
		{
			name:  "empty match/group_by dropped",
			a:     `[{"receiver":"slack","match":{},"group_by":[]}]`,
			b:     `[{"receiver":"slack"}]`,
			equal: true,
		},
		{
			name:  "key order irrelevant",
			a:     `[{"receiver":"slack","matchers":["a=\"b\""]}]`,
			b:     `[{"matchers":["a=\"b\""],"receiver":"slack"}]`,
			equal: true,
		},
		{
			name:  "continue:true is preserved (real difference)",
			a:     `[{"receiver":"slack","continue":true}]`,
			b:     `[{"receiver":"slack"}]`,
			equal: false,
		},
		{
			name:  "different receiver is drift",
			a:     `[{"receiver":"slack"}]`,
			b:     `[{"receiver":"pagerduty"}]`,
			equal: false,
		},
		{
			name:  "order of routes is significant",
			a:     `[{"receiver":"a"},{"receiver":"b"}]`,
			b:     `[{"receiver":"b"},{"receiver":"a"}]`,
			equal: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := RoutesJSONValue{StringValue: basetypes.NewStringValue(tt.a)}
			right := RoutesJSONValue{StringValue: basetypes.NewStringValue(tt.b)}
			got, diags := left.StringSemanticEquals(context.Background(), right)
			if diags.HasError() {
				t.Fatalf("unexpected diags: %v", diags)
			}
			if got != tt.equal {
				t.Errorf("StringSemanticEquals(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.equal)
			}
		})
	}
}
