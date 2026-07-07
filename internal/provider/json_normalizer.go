package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// JSONStringType is a custom string type that implements semantic equality
// based on JSON document equality rather than byte-for-byte string equality.
//
// # Why a custom type
//
// User-authored JSON via HCL `jsonencode()` typically preserves declaration
// order. After a Read round-trip through encoding/json, map keys are
// alphabetised. Without semantic equality, every Read would report drift
// because `{"b":1,"a":2}` ≠ `{"a":2,"b":1}` as strings even though they
// represent the same JSON document.
//
// This type lets the provider store either form in state and treat them as
// equal during the plan phase, eliminating spurious diffs while still
// catching real content changes.
//
// # Implementation
//
// Implements StringTypable + StringValuableWithSemanticEquals from the
// terraform-plugin-framework basetypes package. The semantic-equality
// function parses both sides as JSON and compares the parsed structures
// via fmt.Sprintf("%v") on the json.Marshal output (which is canonical).
type JSONStringType struct {
	basetypes.StringType
}

// JSONStringTypeInstance is the singleton type value used by schema
// declarations. Pass this as `CustomType:` on schema.StringAttribute.
var JSONStringTypeInstance = JSONStringType{}

// String returns the type name shown in framework error messages.
func (t JSONStringType) String() string {
	return "JSONStringType"
}

// ValueType returns the corresponding Value implementation.
func (t JSONStringType) ValueType(_ context.Context) attr.Value {
	return JSONStringValue{}
}

// Equal compares two type values. Two JSONStringTypes are always equal.
func (t JSONStringType) Equal(o attr.Type) bool {
	_, ok := o.(JSONStringType)
	return ok
}

// ValueFromString wraps a basetypes.StringValue in our custom Value type.
func (t JSONStringType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return JSONStringValue{StringValue: in}, nil
}

// ValueFromTerraform converts a raw tftypes value into our custom Value
// implementation. Required by attr.Type.
func (t JSONStringType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrVal, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	stringVal, ok := attrVal.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("expected basetypes.StringValue, got %T", attrVal)
	}
	return JSONStringValue{StringValue: stringVal}, nil
}

// JSONStringValue is the custom string value type used for JSON-bearing
// attributes. It embeds basetypes.StringValue and overrides the equality
// behaviour to compare parsed JSON documents rather than byte strings.
type JSONStringValue struct {
	basetypes.StringValue
}

// Type returns the matching type instance.
func (v JSONStringValue) Type(_ context.Context) attr.Type {
	return JSONStringTypeInstance
}

// Equal performs the framework's exact-equality check. Used by framework
// internals; semantic equality is implemented separately via
// StringSemanticEquals.
func (v JSONStringValue) Equal(o attr.Value) bool {
	other, ok := o.(JSONStringValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals returns true if the two values represent the same
// JSON document. Both sides are parsed and re-marshalled via encoding/json
// (which sorts map keys alphabetically); the resulting bytes are then
// compared. Parse failures fall back to plain string equality so terraform
// can still surface a useful diff if the user pastes invalid JSON.
func (v JSONStringValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	other, ok := newValuable.(JSONStringValue)
	if !ok {
		diags.AddError("invalid semantic equality comparison",
			fmt.Sprintf("JSONStringValue cannot compare against %T", newValuable))
		return false, diags
	}
	if v.IsNull() || other.IsNull() {
		return v.IsNull() == other.IsNull(), diags
	}
	if v.IsUnknown() || other.IsUnknown() {
		return false, diags
	}

	left, err := canonicaliseJSON(v.ValueString())
	if err != nil {
		// Fall back to plain string comparison if either side fails to parse.
		return v.ValueString() == other.ValueString(), diags
	}
	right, err := canonicaliseJSON(other.ValueString())
	if err != nil {
		return v.ValueString() == other.ValueString(), diags
	}
	return left == right, diags
}

// canonicaliseJSON parses a JSON document and re-emits it via encoding/json,
// which sorts map keys alphabetically and uses fixed numeric formatting.
// Works for both objects and arrays at the top level.
func canonicaliseJSON(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// RoutesJSONType is the custom string type for the alertmanager routes_json
// attribute. It extends JSONStringType's semantic JSON equality with
// zero-value normalisation.
//
// # Why routes need more than JSONStringType
//
// Receiver and global bodies are stored server-side as opaque maps, so their
// echo preserves whatever the user wrote. Routes are different: kupe-api
// decodes the list into typed `[]*alertmanager.Route` structs whose fields
// are all `omitempty`, then re-marshals. Any field the user writes with a Go
// zero value — the common one being an explicit `"continue": false`, also
// `"group_by": []`, `"match": {}` — is dropped from the echo and from
// subsequent GETs. Comparing the user's `{"continue":false}` against the
// server's `{}` as raw JSON would report perpetual drift.
//
// Stripping JSON zero-values (false, "", null, [], {}) from both sides
// before comparing mirrors the server's omitempty behaviour, so a Read of
// the stripped echo produces no false drift while genuine content changes
// still surface (MEDIUM-4).
type RoutesJSONType struct {
	basetypes.StringType
}

// RoutesJSONTypeInstance is the singleton type value used by the routes
// schema declaration.
var RoutesJSONTypeInstance = RoutesJSONType{}

// String returns the type name shown in framework error messages.
func (t RoutesJSONType) String() string { return "RoutesJSONType" }

// ValueType returns the corresponding Value implementation.
func (t RoutesJSONType) ValueType(_ context.Context) attr.Value { return RoutesJSONValue{} }

// Equal compares two type values. Two RoutesJSONTypes are always equal.
func (t RoutesJSONType) Equal(o attr.Type) bool {
	_, ok := o.(RoutesJSONType)
	return ok
}

// ValueFromString wraps a basetypes.StringValue in our custom Value type.
func (t RoutesJSONType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return RoutesJSONValue{StringValue: in}, nil
}

// ValueFromTerraform converts a raw tftypes value into our custom Value.
func (t RoutesJSONType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrVal, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	stringVal, ok := attrVal.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("expected basetypes.StringValue, got %T", attrVal)
	}
	return RoutesJSONValue{StringValue: stringVal}, nil
}

// RoutesJSONValue is the custom string value used for routes_json.
type RoutesJSONValue struct {
	basetypes.StringValue
}

// Type returns the matching type instance.
func (v RoutesJSONValue) Type(_ context.Context) attr.Type { return RoutesJSONTypeInstance }

// Equal performs the framework's exact-equality check.
func (v RoutesJSONValue) Equal(o attr.Value) bool {
	other, ok := o.(RoutesJSONValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals compares two routes_json values as JSON documents
// with zero-values stripped from both sides, so an explicit `continue:false`
// (or other omitempty-dropped fields) does not read as drift against the
// server's stripped echo.
func (v RoutesJSONValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	other, ok := newValuable.(RoutesJSONValue)
	if !ok {
		diags.AddError("invalid semantic equality comparison",
			fmt.Sprintf("RoutesJSONValue cannot compare against %T", newValuable))
		return false, diags
	}
	if v.IsNull() || other.IsNull() {
		return v.IsNull() == other.IsNull(), diags
	}
	if v.IsUnknown() || other.IsUnknown() {
		return false, diags
	}

	left, err := canonicaliseRoutesJSON(v.ValueString())
	if err != nil {
		return v.ValueString() == other.ValueString(), diags
	}
	right, err := canonicaliseRoutesJSON(other.ValueString())
	if err != nil {
		return v.ValueString() == other.ValueString(), diags
	}
	return left == right, diags
}

// canonicaliseRoutesJSON parses a routes document, strips JSON zero-values,
// and re-emits it with alphabetised keys.
func canonicaliseRoutesJSON(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(stripJSONZeroValues(v))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// stripJSONZeroValues recursively removes object fields whose value is a JSON
// zero-value (false, "", null, [], {}), mirroring the effect of kupe-api's
// omitempty struct tags on the routes echo. Array elements are kept
// positionally — routes are ordered — but each element is stripped in turn.
func stripJSONZeroValues(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			cs := stripJSONZeroValues(child)
			if isJSONZeroValue(cs) {
				continue
			}
			out[k] = cs
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = stripJSONZeroValues(child)
		}
		return out
	default:
		return v
	}
}

// isJSONZeroValue reports whether a decoded JSON value is a zero-value that
// kupe-api's omitempty tags would drop. Numbers are deliberately not treated
// as zero-values: the Alertmanager Route schema has no numeric fields, so a
// number here is user data that must be preserved verbatim.
func isJSONZeroValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}
