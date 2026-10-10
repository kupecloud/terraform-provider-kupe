package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

func TestAPIErrorDetail(t *testing.T) {
	// Sentinels picked deliberately:
	//   - roleHint is the substring uniquely produced by the 403/RBAC
	//     branch; presence/absence asserts whether we tagged on a
	//     credentials-focused hint.
	//   - schemaHint was the old 400 wording that mislead users hitting
	//     admission-webhook denials. After the rework the 400 branch
	//     emits only the verbatim API message, so any 400 case must
	//     NOT contain this sentinel.
	const (
		roleHint   = "lack the role required"
		schemaHint = "Check the schema"
	)

	tests := []struct {
		name         string
		err          error
		wantSubstr   []string // every entry must appear in the detail
		wantNoSubstr []string // none of these may appear
	}{
		{
			name:         "non-api errors pass through unchanged",
			err:          errors.New("network: connection refused"),
			wantSubstr:   []string{"network: connection refused"},
			wantNoSubstr: []string{"kupe API returned", roleHint},
		},
		{
			// Webhook denial that now arrives as 400. The base line must
			// surface the verbatim message; no schema hint, no role hint.
			name: "400 webhook denial shows verbatim message only",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    `unsupported Kubernetes version "1.32", supported versions: [1.35.5 1.34.8 1.33.12]`,
			},
			wantSubstr: []string{
				"kupe API returned 400",
				`unsupported Kubernetes version "1.32"`,
			},
			wantNoSubstr: []string{schemaHint, roleHint},
		},
		{
			// Kubernetes field causes arrive inside the 400 message and
			// must reach the user verbatim.
			name: "400 validation keeps field causes",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    `invalid database "orders-db": spec.resources.storage: Invalid value: "string": storage can only grow`,
			},
			wantSubstr: []string{"spec.resources.storage", "storage can only grow"},
		},
		{
			name: "404 databases_unavailable says the feature is off, not that the resource is gone",
			err: &client.APIError{
				StatusCode: http.StatusNotFound,
				Message:    "managed databases are not available on this platform",
				Code:       client.CodeDatabasesUnavailable,
			},
			wantSubstr:   []string{"Databases are not enabled for this Kupe Cloud environment", "Error code: databases_unavailable"},
			wantNoSubstr: []string{"does not exist on the server"},
		},
		{
			name: "400 creation_paused says nothing was created and to retry later",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    "Database creation is temporarily paused while we expand platform capacity. Please try again later.",
				Code:       client.CodeCreationPaused,
			},
			wantSubstr:   []string{"Error code: creation_paused", "Creation is temporarily paused on the Kupe platform, so nothing was created", "Existing resources are not affected", "apply` again later"},
			wantNoSubstr: []string{"databases"},
		},
		{
			name: "400 storage_shrink_unsupported says storage only grows",
			err: &client.APIError{
				StatusCode: http.StatusBadRequest,
				Message:    "storage can only grow",
				Code:       client.CodeStorageShrinkUnsupported,
				Field:      "resources.storage",
			},
			wantSubstr: []string{"Error code: storage_shrink_unsupported", "Field: resources.storage", "storage can only grow. Keep the current size"},
		},
		{
			// True RBAC denial: bare "access denied" message from kupe-api.
			// The role hint is the actionable signal — keep it.
			name: "403 access denied shows role hint",
			err: &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    "access denied",
			},
			wantSubstr:   []string{"kupe API returned 403", roleHint},
			wantNoSubstr: nil,
		},
		{
			// Defensive: an older kupe-api that still routes webhook
			// denials through 403 returns a non-"access denied" message.
			// We must NOT show the role hint in that case — that's the
			// exact misleading behaviour this rework is fixing.
			name: "403 non-RBAC body suppresses role hint",
			err: &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    `unsupported Kubernetes version "1.32"`,
			},
			wantSubstr:   []string{"kupe API returned 403", `unsupported Kubernetes version`},
			wantNoSubstr: []string{roleHint},
		},
		{
			// Whitespace/case variations of the RBAC sentinel still count
			// as RBAC — the constant is normalised before comparison.
			name: "403 access denied with surrounding whitespace still tagged",
			err: &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    "  Access Denied  ",
			},
			wantSubstr: []string{roleHint},
		},
		{
			name: "401 carries credentials hint",
			err: &client.APIError{
				StatusCode: http.StatusUnauthorized,
				Message:    "invalid token",
			},
			wantSubstr: []string{"kupe API returned 401", "KUPE_API_KEY"},
		},
		{
			name: "404 carries refresh hint",
			err: &client.APIError{
				StatusCode: http.StatusNotFound,
				Message:    `cluster "smoke" not found`,
			},
			wantSubstr: []string{"kupe API returned 404", "terraform refresh"},
		},
		{
			name: "wrapped api error is unwrapped",
			err: fmt.Errorf("client: %w", &client.APIError{
				StatusCode: http.StatusForbidden,
				Message:    "access denied",
			}),
			wantSubstr: []string{"kupe API returned 403", roleHint},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := apiErrorDetail(tt.err)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("missing substring %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.wantNoSubstr {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected substring %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

func TestDeleteErrorDetail(t *testing.T) {
	const (
		wrapper        = "delete not accepted before the timeout"
		deleteHint     = "kept refusing the delete"
		createConflict = "a resource with this name already exists"
	)
	conflict := fakeAPIError(http.StatusConflict, "the database changed while it was being deleted, retry")
	tests := []struct {
		name         string
		err          error
		wantSubstr   []string
		wantNoSubstr []string
	}{
		{
			name:         "a conflict that outlasted the timeout keeps the wrapper and gets the delete hint",
			err:          fmt.Errorf("%w: %w", errDeleteNotAccepted, conflict),
			wantSubstr:   []string{wrapper, "kupe API returned 409", deleteHint, "Run the same `terraform` command again"},
			wantNoSubstr: []string{createConflict},
		},
		{
			name:         "another error that outlasted the timeout keeps the wrapper and its own hint",
			err:          fmt.Errorf("%w: %w", errDeleteNotAccepted, fakeAPIError(http.StatusServiceUnavailable, "unavailable")),
			wantSubstr:   []string{wrapper, "kupe API returned 503", "temporarily unavailable"},
			wantNoSubstr: []string{deleteHint},
		},
		{
			name:         "a non-API error that outlasted the timeout is not prefixed twice",
			err:          fmt.Errorf("%w: %w", errDeleteNotAccepted, errors.New("connection refused")),
			wantSubstr:   []string{wrapper + ": connection refused"},
			wantNoSubstr: []string{wrapper + ": " + wrapper},
		},
		{
			name:         "an error returned at once is described as usual",
			err:          fakeAPIError(http.StatusForbidden, "access denied"),
			wantSubstr:   []string{"kupe API returned 403", "lack the role required"},
			wantNoSubstr: []string{wrapper, deleteHint},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deleteErrorDetail(tt.err)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("missing substring %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.wantNoSubstr {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected substring %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

// TestDeleteUntilAccepted_KeepsRefusalWhenCutOff: the attempt in flight when
// the timeout fires fails with a context error; the error returned must
// still be the refusal that kept the delete from being accepted.
func TestDeleteUntilAccepted_KeepsRefusalWhenCutOff(t *testing.T) {
	shrinkPollInterval(t)
	conflict := fakeAPIError(http.StatusConflict, "the database changed while it was being deleted, retry")
	calls := 0
	err := deleteUntilAccepted(context.Background(), 50*time.Millisecond, func(c context.Context) error {
		calls++
		if calls < 3 {
			return conflict
		}
		<-c.Done()
		return fmt.Errorf("executing request: %w", c.Err())
	})
	if !errors.Is(err, errDeleteNotAccepted) || !errors.Is(err, conflict) {
		t.Fatalf("expected the conflict wrapped as not accepted, got %v", err)
	}
}

// fakeAPIError returns a client.APIError shaped to match what the real
// client surfaces from a server response with the given status code.
func fakeAPIError(status int, msg string) error {
	return &client.APIError{StatusCode: status, Message: msg}
}

// TestIsTerminalAPIError pins which statuses a readiness/deletion poll
// treats as permanent (401/403/400 — surface immediately) versus transient
// (keep polling). Regression guard for LOW-1: before the fix every GET
// error inside the poll closures was swallowed, so a revoked key stalled to
// the full timeout and hid the real 401/403 behind a generic warning.
func TestIsTerminalAPIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not terminal", nil, false},
		{"400 is terminal", fakeAPIError(http.StatusBadRequest, "bad request"), true},
		{"401 is terminal", fakeAPIError(http.StatusUnauthorized, "invalid token"), true},
		{"403 is terminal", fakeAPIError(http.StatusForbidden, "access denied"), true},
		{"404 is not terminal", fakeAPIError(http.StatusNotFound, "not found"), false},
		{
			"404 databases_unavailable is terminal",
			&client.APIError{StatusCode: http.StatusNotFound, Code: client.CodeDatabasesUnavailable},
			true,
		},
		{"409 is not terminal", fakeAPIError(http.StatusConflict, "already exists"), false},
		{"500 is not terminal", fakeAPIError(http.StatusInternalServerError, "boom"), false},
		{"wrapped 403 is terminal", fmt.Errorf("get: %w", fakeAPIError(http.StatusForbidden, "access denied")), true},
		{"non-api error is not terminal", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTerminalAPIError(tt.err); got != tt.want {
				t.Errorf("isTerminalAPIError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// shrinkPollInterval lowers the helper's poll interval so the tests run
// in millisecond-scale rather than seconds. Tests restore the original
// in a t.Cleanup.
func shrinkPollInterval(t *testing.T) {
	t.Helper()
	prev := defaultPollInterval
	defaultPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { defaultPollInterval = prev })
}

func TestWaitForCondition_DoneOnFirstCheck(t *testing.T) {
	// Resource is already in the desired state by the time we poll —
	// the function must return immediately without waiting an entire
	// poll interval. Mirrors the "delete that already completed by
	// the time apply hits the GET" case.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected nil on immediate done, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("expected immediate return, took %v", elapsed)
	}
}

func TestWaitForCondition_DoneAfterRetries(t *testing.T) {
	// Resource reaches the target state after several polls. Verifies
	// the loop converges rather than the exact timing.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	calls := 0
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		calls++
		return calls >= 3, nil
	})
	if err != nil {
		t.Fatalf("expected nil after convergence, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 calls, got %d", calls)
	}
}

func TestWaitForCondition_TimeoutSurfacesContextError(t *testing.T) {
	// Predicate never becomes true — context deadline bounds the wait
	// and the helper returns a wrapped DeadlineExceeded so callers can
	// map it to a user-facing warning.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected error to wrap context.DeadlineExceeded, got %v", err)
	}
}

func TestWaitForCondition_TerminalErrorSurfaces(t *testing.T) {
	// done=true with a non-nil err signals a terminal failure (a
	// future "phase=Degraded with reason" path could use this). The
	// helper must surface that error rather than returning nil.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	terminalErr := errors.New("resource entered terminal failure state")
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		return true, terminalErr
	})
	if !errors.Is(err, terminalErr) {
		t.Fatalf("expected the terminal error to surface, got %v", err)
	}
}

func TestWaitForCondition_DeletePredicate(t *testing.T) {
	// Realistic delete-flavoured usage: closure calls a GET and
	// reports done=true when the API returns 404. Mirrors what the
	// cluster/secret Delete handlers do internally.
	shrinkPollInterval(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	calls := 0
	err := waitForCondition(ctx, func(context.Context) (bool, error) {
		calls++
		// First two polls see the resource still terminating; third
		// sees the 404. The closure swallows the non-NotFound err
		// (transient) and returns done=false.
		if calls < 3 {
			return false, nil
		}
		if !client.IsNotFound(fakeAPIError(http.StatusNotFound, "not found")) {
			t.Fatal("test setup: IsNotFound predicate broken")
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected nil after the resource disappeared, got %v", err)
	}
}

func TestDeleteUntilAccepted(t *testing.T) {
	conflict := fakeAPIError(http.StatusConflict, "the object has been modified")
	protected := &client.APIError{StatusCode: http.StatusConflict, Code: client.CodeDeletionProtected}
	tests := []struct {
		name      string
		responses []error // one per attempt; the last repeats
		wantErr   error   // matched with errors.Is; nil means success
		wantCalls int
	}{
		{name: "accepted at once", responses: []error{nil}, wantCalls: 1},
		{name: "already gone", responses: []error{fakeAPIError(http.StatusNotFound, "not found")}, wantCalls: 1},
		{name: "conflicts are retried", responses: []error{conflict, conflict, nil}, wantCalls: 3},
		{name: "deletion protection is returned at once", responses: []error{protected}, wantErr: protected, wantCalls: 1},
		{name: "a persistent conflict returns the last refusal", responses: []error{conflict}, wantErr: conflict},
		{name: "a persistent conflict is marked not accepted", responses: []error{conflict}, wantErr: errDeleteNotAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shrinkPollInterval(t)
			calls := 0
			err := deleteUntilAccepted(context.Background(), 50*time.Millisecond, func(context.Context) error {
				resp := tt.responses[min(calls, len(tt.responses)-1)]
				calls++
				return resp
			})
			if tt.wantErr == nil && err != nil {
				t.Fatalf("expected success, got %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
			if tt.wantCalls != 0 && calls != tt.wantCalls {
				t.Errorf("delete called %d times, want %d", calls, tt.wantCalls)
			}
		})
	}
}
