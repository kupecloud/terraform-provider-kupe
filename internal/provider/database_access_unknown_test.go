package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestDatabaseBindingCredentialsUnknownSafe(t *testing.T) {
	ctx := context.Background()
	r := NewDatabaseBindingResource().(*DatabaseBindingResource)
	s := testResourceSchema(t, r)
	credentialsType := s.Schema.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["credentials"]
	for _, test := range []struct {
		name                        string
		role, roleName, credentials any
	}{
		{"role unresolved", tftypes.UnknownValue, nil, nil},
		{"credentials unresolved", "custom", tftypes.UnknownValue, tftypes.UnknownValue},
		{"dedicated unresolved", "custom", tftypes.UnknownValue, map[string]tftypes.Value{"dedicated": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue), "rotation_trigger": tftypes.NewValue(tftypes.String, "ticket")}},
		{"shared defaults", "readonly", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := testPlanValue(t, s, map[string]tftypes.Value{"role": tftypes.NewValue(tftypes.String, test.role), "role_name": tftypes.NewValue(tftypes.String, test.roleName), "credentials": tftypes.NewValue(credentialsType, test.credentials)})
			var response resource.ValidateConfigResponse
			r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s.Schema, Raw: raw}}, &response)
			if response.Diagnostics.HasError() {
				t.Fatalf("unresolved/null configuration rejected: %v", response.Diagnostics)
			}
		})
	}
}

func TestDatabaseAccessSchemaContainsNoPassword(t *testing.T) {
	ctx := context.Background()
	r := NewDatabaseBindingResource()
	s := testResourceSchema(t, r)
	attributes := s.Schema.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes
	credentials := attributes["credentials"].(tftypes.Object).AttributeTypes
	if len(credentials) != 2 || credentials["dedicated"] == nil || credentials["rotation_trigger"] == nil {
		t.Fatalf("credential state contains unexpected fields: %v", credentials)
	}
	for _, field := range []string{"password", "username", "uri"} {
		if _, ok := attributes[field]; ok {
			t.Errorf("credential material %s in state schema", field)
		}
	}
}

func TestDatabasePlanKeepsComputedUnknownForUnresolvedSettings(t *testing.T) {
	ctx := context.Background()
	r := NewDatabaseResource().(*DatabaseResource)
	s := testResourceSchema(t, r)
	attributes := s.Schema.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes
	for _, field := range []string{"unchanged", "postgres", "backup", "version"} {
		t.Run(field, func(t *testing.T) {
			values := map[string]tftypes.Value{
				"name": tftypes.NewValue(tftypes.String, "orders-db"), "engine": tftypes.NewValue(tftypes.String, "postgres"), "version": tftypes.NewValue(tftypes.String, "18"),
				"high_availability": tftypes.NewValue(tftypes.Bool, false), "deletion_protection": tftypes.NewValue(tftypes.Bool, false),
				"etag": tftypes.NewValue(tftypes.String, "original-etag"), "phase": tftypes.NewValue(tftypes.String, "Running"),
			}
			for _, key := range []string{"postgres", "backup", "source"} {
				values[key] = tftypes.NewValue(attributes[key], nil)
			}
			stateRaw := testPlanValue(t, s, values)
			values["etag"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
			values["phase"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
			if field != "unchanged" {
				values[field] = tftypes.NewValue(attributes[field], tftypes.UnknownValue)
			}
			plan := tfsdk.Plan{Schema: s.Schema, Raw: testPlanValue(t, s, values)}
			response := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan, State: tfsdk.State{Schema: s.Schema, Raw: stateRaw}}, &response)
			if response.Diagnostics.HasError() {
				t.Fatalf("plan diagnostics: %v", response.Diagnostics)
			}
			var etag types.String
			diagnostics := response.Plan.GetAttribute(ctx, path.Root("etag"), &etag)
			if diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			if field == "unchanged" && etag.ValueString() != "original-etag" {
				t.Fatal("unchanged adopted settings produced a computed-only diff")
			}
			if field != "unchanged" && !etag.IsUnknown() {
				t.Fatalf("unresolved %s incorrectly fixed ETag to previous state", field)
			}
		})
	}
}
