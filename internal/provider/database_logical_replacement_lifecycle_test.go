package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type replacementEvent struct {
	Phase, Method, Path string
	Code, UID           int
	Body                json.RawMessage
}
type replacementOracle struct {
	mu     sync.Mutex
	phase  string
	events []replacementEvent
	uids   map[string]int
	next   int
}

func (o *replacementOracle) phaseTo(p string) { o.mu.Lock(); defer o.mu.Unlock(); o.phase = p }
func (o *replacementOracle) decorate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			body = nil
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		path := r.URL.Path
		at := strings.Index(path, "/databases")
		if at >= 0 {
			rest := strings.Trim(path[at+len("/databases"):], "/")
			if !strings.Contains(rest, "/") {
				o.mu.Lock()
				name := rest
				if r.Method == "POST" && rec.Code == 201 {
					var b map[string]any
					_ = json.Unmarshal(rec.Body.Bytes(), &b)
					name, _ = b["name"].(string)
					o.next++
					o.uids[name] = o.next
				}
				uid := o.uids[name]
				o.events = append(o.events, replacementEvent{o.phase, r.Method, path, rec.Code, uid, body})
				if r.Method == "DELETE" && rec.Code >= 200 && rec.Code < 300 {
					delete(o.uids, name)
				}
				o.mu.Unlock()
			}
		}
		for k, values := range rec.Header() {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}
func (o *replacementOracle) snapshot(t *testing.T, m *mockKupeAPI, label string) {
	t.Helper()
	o.mu.Lock()
	b, _ := json.Marshal(map[string]any{"label": label, "events": o.events, "syntheticFixtureUIDs": o.uids})
	o.mu.Unlock()
	m.mu.Lock()
	objects, _ := json.Marshal(m.databases)
	m.mu.Unlock()
	t.Logf("DELETE_ORACLE %s OBJECTS %s", b, objects)
}
func (o *replacementOracle) counts(phase string) (deletes, patches, posts int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, e := range o.events {
		if e.Phase != phase {
			continue
		}
		switch e.Method {
		case "DELETE":
			deletes++
		case "PATCH":
			patches++
		case "POST":
			posts++
		}
	}
	return
}

type replacementPlanCheck struct {
	t       *testing.T
	unknown bool
	seen    *bool
}

func (c replacementPlanCheck) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	b, _ := json.Marshal(req.Plan)
	var p map[string]any
	_ = json.Unmarshal(b, &p)
	for _, v := range p["resource_changes"].([]any) {
		r := v.(map[string]any)
		if r["address"] != "kupe_database.test" {
			continue
		}
		ch := r["change"].(map[string]any)
		a, _ := json.Marshal(ch["actions"])
		if string(a) != `["delete","create"]` {
			resp.Error = fmt.Errorf("expected independent DestroyBeforeCreate, got %s", a)
		}
		if c.unknown {
			u, _ := ch["after_unknown"].(map[string]any)
			if u["name"] != true {
				resp.Error = fmt.Errorf("metadata name not genuinely unknown: %s", b)
			}
		}
		*c.seen = true
	}
	c.t.Logf("INITIAL_REPLACEMENT_PLAN %s", b)
}

// Adapted from the independent F55-04 lifecycle oracle. UIDs are synthetic
// fixture identities; snapshots distinguish candidate requests from cleanup.
func TestAccDatabaseResource_LogicalReplacementLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name, target, list, input, expect                    string
		unknown, protected, source, omit, schemaReset, taint bool
	}{
		{name: "known distinct replacement", target: `"invoices-db"`, list: `[{name="invoices_db"},{name="analytics"}]`},
		{name: "unknown target known first", target: `terraform_data.instance.output`, input: `"invoices-db"`, list: `[{name="invoices_db"},{name="analytics"}]`, unknown: true},
		{name: "unknown distinct derived first", target: `terraform_data.instance.output`, input: `"invoices-db"`, list: `[{name=replace(terraform_data.instance.output,"-","_")},{name="analytics"}]`, unknown: true},
		{name: "unknown same target known reset", target: `terraform_data.instance.output`, input: `"orders-db"`, list: `[{name="orders_db"}]`, unknown: true},
		{name: "independent unknown same deferred reset", target: `terraform_data.instance.output`, input: `"orders-db"`, list: `terraform_data.logical.output`, unknown: true},
		{name: "literal same removal refused", target: `"orders-db"`, list: `[{name="orders_db"}]`, expect: `cannot be removed`},

		{name: "literal same rename refused", target: `"orders-db"`, list: `[{name="orders_db"},{name="renamed",schemas=[{name="reporting"}]}]`, expect: `cannot be removed`},
		{name: "literal same schema shrink refused", target: `"orders-db"`, list: `[{name="orders_db"},{name="analytics",schemas=[]}]`, expect: `cannot be removed`},
		{name: "literal same first movement refused", target: `"orders-db"`, list: `[{name="analytics",schemas=[{name="reporting"}]},{name="orders_db"}]`, expect: `First PostgreSQL database cannot change`},
		{name: "protected replacement refused before new POST", target: `"invoices-db"`, list: `[{name="invoices_db"}]`, expect: `deletion protection`, protected: true},
		{name: "invalid resolved metadata", target: `terraform_data.instance.output`, input: `"bad_name"`, list: `[{name=replace(terraform_data.instance.output,"-","_")},{name="analytics"}]`, unknown: true, expect: `Invalid Attribute Value|Database name unavailable`},
		{name: "source-only replacement omitted lists", target: `"orders-db"`, source: true, omit: true},
		{name: "source-only replacement logical override refused", target: `"orders-db"`, list: logicalTwo, source: true, expect: `Restore database settings after creation`},
		{name: "known invalid literal metadata", target: `"bad_name"`, list: `[{name="bad_name"},{name="analytics"}]`, expect: `Invalid Attribute Value`},
		{name: "known new target wrong first", target: `"invoices-db"`, list: `[{name="wrong"},{name="analytics"}]`, expect: `First PostgreSQL database cannot change`},
		{name: "compound unknown replacement schema reset", target: `terraform_data.instance.output`, input: `"invoices-db"`, list: `[{name=replace(terraform_data.instance.output,"-","_")},{name="analytics",schemas=terraform_data.schemas.output}]`, unknown: true, schemaReset: true},
		{name: "explicit tainted same metadata reset", target: `"orders-db"`, list: `[{name="orders_db"}]`, taint: true},
		{name: "explicit tainted unchanged list control", target: `"orders-db"`, list: logicalTwo, taint: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := &replacementOracle{phase: "initial", uids: map[string]int{}}
			m := newMockKupeAPI(o.decorate)
			defer m.close()
			shrinkPollInterval(t)
			t.Cleanup(func() { o.snapshot(t, m, "after fixture cleanup") })
			initial := logicalConfig(m, logicalTwo)
			if tt.protected {
				initial = strings.Replace(initial, "deletion_protection = false", "deletion_protection = true", 1)
			}
			nextConfig := logicalConfig(m, tt.list)
			if tt.omit {
				nextConfig = testAccDatabaseConfig(m.url(), testDatabase{protection: boolPtr(false)})
			}
			cfg := strings.Replace(nextConfig, `name = "orders-db"`, `name = `+tt.target, 1)
			if tt.input != "" {
				cfg += "\n" + `resource "terraform_data" "instance" {input=` + tt.input + `}`
			}
			if strings.Contains(tt.list, "logical.output") {
				cfg += "\n" + `resource "terraform_data" "logical" {input=[{name="orders_db"}]}`
			}
			if tt.schemaReset {
				cfg += "\n" + `resource "terraform_data" "schemas" {input=[]}`
			}
			if tt.source {
				cfg = strings.Replace(cfg, `name = "orders-db"`, `name = "orders-db"`+"\nsource={database=\"source-db\"}", 1)
			}
			seen := false
			oldUID := 0
			candidate := resource.TestStep{PreConfig: func() { o.phaseTo("candidate") }, Config: cfg}
			if tt.taint {
				candidate.Taint = []string{"kupe_database.test"}
			}
			if tt.expect != "" {
				candidate.ExpectError = regexp.MustCompile(tt.expect)
				if tt.unknown {
					candidate.ConfigPlanChecks = resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{replacementPlanCheck{t, true, &seen}}}
				}
			} else {
				candidate.ConfigPlanChecks = resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{replacementPlanCheck{t, tt.unknown, &seen}}}
				candidate.Check = func(state *terraform.State) error {
					if tt.schemaReset {
						if err := resource.TestCheckResourceAttr("kupe_database.test", "postgres.databases.1.schemas.#", "0")(state); err != nil {
							return err
						}
					}
					o.snapshot(t, m, "successful replacement before teardown")
					d, p, n := o.counts("candidate")
					if !seen || d != 1 || p != 0 || n != 1 {
						return fmt.Errorf("replacement oracle seen=%t DELETE=%d PATCH=%d POST=%d", seen, d, p, n)
					}
					name := strings.Trim(tt.target, `"`)
					if tt.input != "" {
						name = strings.Trim(tt.input, `"`)
					}
					o.mu.Lock()
					uid := o.uids[name]
					o.mu.Unlock()
					if uid == 0 || uid == oldUID {
						return fmt.Errorf("replacement did not create a new synthetic fixture UID: %d→%d", oldUID, uid)
					}
					o.phaseTo("teardown")
					return nil
				}
			}
			base := resource.TestStep{Config: initial, Check: func(*terraform.State) error {
				if tt.source {
					m.mu.Lock()
					encoded, _ := json.Marshal(m.databases["orders-db"])
					var copy map[string]any
					_ = json.Unmarshal(encoded, &copy)
					copy["name"] = "source-db"
					m.databases["source-db"] = copy
					m.mu.Unlock()
				}
				o.mu.Lock()
				oldUID = o.uids["orders-db"]
				o.mu.Unlock()
				return nil
			}}
			steps := []resource.TestStep{base, candidate}
			if tt.expect != "" {
				cleanup := logicalConfig(m, logicalTwo)
				if tt.input != "" {
					cleanup += `resource "terraform_data" "instance" {input=` + tt.input + `}`
				}
				steps = append(steps, resource.TestStep{PreConfig: func() {
					o.snapshot(t, m, "expected refusal BEFORE explicit cleanup")
					d, p, n := o.counts("candidate")
					if p != 0 || n != 0 {
						t.Errorf("refusal wrote before cleanup: PATCH=%d POST=%d", p, n)
					}
					if !tt.unknown && !tt.protected {
						if d != 0 {
							t.Errorf("known invalid configuration deleted old object: %d", d)
						}
						o.mu.Lock()
						uid := o.uids["orders-db"]
						o.mu.Unlock()
						if uid != oldUID {
							t.Errorf("known refusal lost old UID: %d→%d", oldUID, uid)
						}
					}
					if tt.unknown {
						if d != 1 {
							t.Errorf("final-invalid replacement DELETE attempts=%d", d)
						}
						m.mu.Lock()
						old := m.databases["orders-db"]
						m.mu.Unlock()
						if old != nil {
							t.Error("expected existing replacement lifecycle to have deleted old object before final invalid error")
						}
					}
					if tt.protected {
						if d != 1 {
							t.Errorf("protected DELETE attempts=%d", d)
						}
						m.mu.Lock()
						old := m.databases["orders-db"]
						protected := old != nil && old["deletionProtection"] == true
						m.mu.Unlock()
						if !protected {
							t.Error("old protected object lost or automatically unprotected")
						}
					}
					o.phaseTo("explicit cleanup")
				}, Config: cleanup, Check: func(*terraform.State) error { o.phaseTo("teardown"); return nil }})
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories(), ErrorCheck: func(err error) error {
				o.snapshot(t, m, "unexpected error BEFORE fixture teardown")
				o.phaseTo("failed-step teardown")
				return err
			}, Steps: steps})
		})
	}
}
