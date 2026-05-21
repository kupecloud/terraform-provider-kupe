package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// fakeAPIError returns a client.APIError shaped to match what the real
// client surfaces from a server response with the given status code.
func fakeAPIError(status int, msg string) error {
	return &client.APIError{StatusCode: status, Message: msg}
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
