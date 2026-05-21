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

func TestWaitForDeleteGone_AlreadyGone(t *testing.T) {
	// 404 on the first call means deletion was already complete by
	// the time we started polling — the function must return nil
	// without waiting an entire poll interval.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := waitForDeleteGone(ctx, func(context.Context) error {
		return fakeAPIError(http.StatusNotFound, "not found")
	})
	if err != nil {
		t.Fatalf("expected nil on immediate 404, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("expected immediate return, took %v", elapsed)
	}
}

func TestWaitForDeleteGone_GoneAfterRetries(t *testing.T) {
	// Resource exists for a few polls, then disappears. We need to
	// outlast at least one ticker fire — the helper's interval is 2s
	// by default which is too slow for unit tests, but the public
	// constant means we just give the test enough time to see one
	// tick. The test is mostly verifying the loop converges rather
	// than the exact timing.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calls := 0
	err := waitForDeleteGone(ctx, func(context.Context) error {
		calls++
		if calls < 3 {
			// Still terminating
			return nil
		}
		return fakeAPIError(http.StatusNotFound, "not found")
	})
	if err != nil {
		t.Fatalf("expected nil after the resource disappeared, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 GET calls before success, got %d", calls)
	}
}

func TestWaitForDeleteGone_TimeoutSurfacesContextError(t *testing.T) {
	// Resource never disappears — context deadline should bound the
	// wait and surface a wrapped context error so the caller can map
	// it to a user-facing warning.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := waitForDeleteGone(ctx, func(context.Context) error {
		// Still here forever — return nil to indicate "exists".
		return nil
	})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected error to wrap context.DeadlineExceeded, got %v", err)
	}
}

func TestWaitForDeleteGone_TransientErrorsAreToleratedUntilTimeout(t *testing.T) {
	// A persistent 5xx during polling shouldn't immediately fail the
	// destroy — we keep retrying until either the resource disappears
	// or the timeout elapses. A short timeout here verifies the loop
	// continues despite non-NotFound errors.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := waitForDeleteGone(ctx, func(context.Context) error {
		return fakeAPIError(http.StatusInternalServerError, "transient")
	})
	if err == nil {
		t.Fatal("expected timeout when polling always sees 5xx")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}
