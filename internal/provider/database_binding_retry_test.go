package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// A status-only write can invalidate an ETag without changing settings. A
// concurrent credential-mode change must instead refuse the namespace PATCH:
// accepting its response would adopt an identity outside this apply's plan.
func TestDatabaseBindingUpdateRetryConcurrentCredentials(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		name := "status-only-control"
		if dedicated {
			name = "concurrent-dedicated-change"
		}
		t.Run(name, func(t *testing.T) {
			current := client.DatabaseBinding{Database: "orders-db", Name: "binding", Namespaces: []string{"api"}, Credentials: client.BindingCredentials{Dedicated: dedicated}}
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				if r.Method != http.MethodGet {
					t.Errorf("unexpected mutation: %s", r.Method)
				}
				w.Header().Set("ETag", "fresh-etag")
				if err := json.NewEncoder(w).Encode(current); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			resource := NewDatabaseBindingResource().(*DatabaseBindingResource)
			resource.client = client.New(server.URL, "acme", "test-generated-token")
			prior := DatabaseBindingResourceModel{
				Database: types.StringValue("orders-db"), Name: types.StringValue("binding"),
				Cluster: types.StringValue("apps"), Role: types.StringValue("readwrite"),
				Namespaces:  stringSet([]string{"api"}),
				Credentials: bindingCredentialsValue(false, types.StringNull()),
			}
			saved := prior
			sends := 0
			send := func(_ context.Context, etag string) (*client.DatabaseBinding, string, error) {
				sends++
				if sends == 1 {
					if etag != "old-etag" {
						t.Errorf("initial ETag = %q", etag)
					}
					return nil, "", &client.APIError{StatusCode: http.StatusPreconditionFailed}
				}
				if etag != "fresh-etag" {
					t.Errorf("retry ETag = %q", etag)
				}
				return &current, "accepted-etag", nil
			}
			obj, etag, err := resource.updateWithRetry(context.Background(), &prior, "old-etag", send)
			if dedicated {
				var changed *concurrentChangeError
				if !errors.As(err, &changed) || sends != 1 {
					t.Fatalf("concurrent dedicated change was retried/accepted: err=%v sends=%d", err, sends)
				}
				if !reflect.DeepEqual(changed.fields, []string{"credentials.dedicated"}) {
					t.Errorf("concurrent fields = %v", changed.fields)
				}
				if obj != nil || etag != "" {
					t.Errorf("refused update returned accepted state: obj=%v etag=%q", obj, etag)
				}
			} else if err != nil || sends != 2 || obj != &current || etag != "accepted-etag" {
				t.Fatalf("status-only control should retry successfully: err=%v sends=%d etag=%q", err, sends, etag)
			}
			if reads != 1 {
				t.Errorf("rereads = %d, want 1", reads)
			}
			if !reflect.DeepEqual(prior, saved) {
				t.Error("retry changed prior state or binding identity")
			}
		})
	}
}
