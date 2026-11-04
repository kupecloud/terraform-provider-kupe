package provider

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The API projects an effective list even for a legacy instance. Tests which
// restore an instance record its first identity explicitly in the mock object.
func mockFirstLogical(db map[string]any) string {
	if first := strOrEmpty(db["firstLogicalDatabase"]); first != "" {
		return first
	}
	return strings.ReplaceAll(strOrEmpty(db["name"]), "-", "_")
}
func mockEffectivePostgres(db map[string]any) {
	p, _ := db["postgres"].(map[string]any)
	if p == nil {
		p = map[string]any{}
	}
	if _, ok := p["databases"]; !ok {
		p["databases"] = []any{map[string]any{"name": mockFirstLogical(db), "schemas": []any{}}}
	}
	for _, value := range p["databases"].([]any) {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if entry["schemas"] == nil {
			entry["schemas"] = []any{}
		}
	}
	db["postgres"] = p
}
func mockRequestedLogical(db, body map[string]any) string {
	if n := strOrEmpty(body["logicalDatabase"]); n != "" {
		return n
	}
	return mockFirstLogical(db)
}
func mockLogicalSecret(database, logical, first, role, roleName string) string {
	if role == "custom" {
		role = strings.ReplaceAll(roleName, "_", "-")
	}
	parts := []string{database}
	if logical != "" && logical != first {
		parts = append(parts, strings.ReplaceAll(logical, "_", "-"))
	}
	parts = append(parts, role)
	return strings.Join(parts, "-")
}
func mockLogicalPatch(db map[string]any, requested any) error {
	list, ok := requested.([]any)
	if !ok || len(list) == 0 {
		return fmt.Errorf("postgres.databases cannot be empty or null")
	}
	if strOrEmpty(list[0].(map[string]any)["name"]) != mockFirstLogical(db) {
		return fmt.Errorf("first PostgreSQL database cannot change")
	}
	mockEffectivePostgres(db)
	p := db["postgres"].(map[string]any)
	old, _ := p["databases"].([]any)
	for _, value := range old {
		stored := value.(map[string]any)
		idx := slices.IndexFunc(list, func(v any) bool { return v.(map[string]any)["name"] == stored["name"] })
		if idx < 0 {
			return fmt.Errorf("databases and their schemas cannot be removed or renamed: %q is missing", stored["name"])
		}
		schemas, _ := stored["schemas"].([]any)
		next, _ := list[idx].(map[string]any)["schemas"].([]any)
		for _, schema := range schemas {
			if !slices.ContainsFunc(next, func(s any) bool { return s.(map[string]any)["name"] == schema.(map[string]any)["name"] }) {
				return fmt.Errorf("databases and their schemas cannot be removed or renamed")
			}
		}
	}
	return nil
}

// Generated from the immutable approved API4c projection's exact declarations;
// the extraction manifest and compiled fixture output are retained with 4e evidence.
func TestMockLogicalProjectionDefaults(t *testing.T) {
	const fixturesJSON = `{"existing_schema":{"effective":[{"name":"orders_db","schemas":[]},{"name":"analytics","schemas":[{"name":"reporting"}]}],"raw":{"metadata":{"name":"orders-db"},"spec":{"postgres":{"databases":[{"name":"orders_db"},{"name":"analytics","schemas":[{"name":"reporting"}]}]}}}},"explicit_empty_schemas":{"effective":[{"name":"orders_db","schemas":[]},{"name":"analytics","schemas":[]}],"raw":{"metadata":{"name":"orders-db"},"spec":{"postgres":{"databases":[{"name":"orders_db","schemas":[]},{"name":"analytics","schemas":[]}]}}}},"legacy":{"effective":[{"name":"orders_db","schemas":[]}],"raw":{"metadata":{"name":"orders-db"},"spec":{}}},"new_added_omitted_schemas":{"effective":[{"name":"orders_db","schemas":[]},{"name":"analytics","schemas":[]}],"raw":{"metadata":{"name":"orders-db"},"spec":{"postgres":{"databases":[{"name":"orders_db"},{"name":"analytics"}]}}}},"restored":{"effective":[{"name":"historical_db","schemas":[]}],"raw":{"metadata":{"annotations":{"kupe.cloud/postgres-database":"historical_db"},"name":"orders-copy"},"spec":{}}}}`
	var fixtures map[string]struct {
		Raw       map[string]any `json:"raw"`
		Effective []any          `json:"effective"`
	}
	if err := json.Unmarshal([]byte(fixturesJSON), &fixtures); err != nil {
		t.Fatal(err)
	}
	for name, f := range fixtures {
		t.Run(name, func(t *testing.T) {
			metadata := f.Raw["metadata"].(map[string]any)
			spec := f.Raw["spec"].(map[string]any)
			db := map[string]any{"name": metadata["name"], "postgres": spec["postgres"]}
			if annotations, ok := metadata["annotations"].(map[string]any); ok {
				db["firstLogicalDatabase"] = annotations["kupe.cloud/postgres-database"]
			}
			mockEffectivePostgres(db)
			actual, err := json.Marshal(db["postgres"].(map[string]any)["databases"])
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(f.Effective)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(want) {
				t.Fatalf("mock projection=%s, approved API projection=%s", actual, want)
			}
		})
	}
}
