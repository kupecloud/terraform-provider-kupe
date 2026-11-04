package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogicalDatabaseWirePresence(t *testing.T) {
	for _, tt := range []struct {
		name      string
		databases *[]LogicalDatabase
		want      string
	}{
		{"omitted", nil, `{"postgres":{}}`},
		{"explicit empty", &[]LogicalDatabase{}, `{"postgres":{"databases":[]}}`},
		{"whole list", &[]LogicalDatabase{{Name: "orders_db", Schemas: []DatabaseSchema{}}, {Name: "analytics", Schemas: []DatabaseSchema{{Name: "reports"}}}}, `{"postgres":{"databases":[{"name":"orders_db","schemas":[]},{"name":"analytics","schemas":[{"name":"reports"}]}]}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(PatchDatabaseRequest{Postgres: &DatabasePostgres{Databases: tt.databases}})
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tt.want {
				t.Fatalf("body=%s", body)
			}
		})
	}
}
func TestBindingRestoredFirstIdentityConfirmedBeforeCreate(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if confirmed {
						_, _ = w.Write([]byte(`{"name":"orders-copy","postgres":{"databases":[{"name":"historical_db","schemas":[]}]}}`))
					} else {
						_, _ = w.Write([]byte(`{"name":"orders-copy","source":{"database":"later-source"}}`))
					}
					return
				}
				posts++
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"name":"binding","database":"orders-copy","logicalDatabase":"historical_db","role":"readwrite"}`))
			}))
			defer server.Close()
			c := New(server.URL, "acme", "fake-test-key")
			b, _, err := c.CreateDatabaseBinding(context.Background(), "orders-copy", CreateDatabaseBindingRequest{Name: "binding", LogicalDatabase: "historical_db", Role: "readwrite"})
			if confirmed {
				if err != nil || posts != 1 || b.FirstLogicalDatabase != "historical_db" {
					t.Fatalf("binding=%+v posts=%d err=%v", b, posts, err)
				}
			} else {
				if err == nil || posts != 0 || !strings.Contains(err.Error(), "first PostgreSQL") {
					t.Fatalf("unconfirmed identity allowed create: posts=%d err=%v", posts, err)
				}
			}
		})
	}
}
