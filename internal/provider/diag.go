package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// deletePollInterval is how often waitForDeleteGone re-checks the server.
// 2s gives reasonable feedback latency without hammering kupe-api during a
// multi-minute teardown (vCluster finalizers, namespace cleanup).
const deletePollInterval = 2 * time.Second

// waitForDeleteGone polls a resource-specific GET until it returns a 404
// (resource genuinely gone) or the context expires. Used by resource
// Delete handlers whose backing CR has an operator-managed finalizer
// (managedcluster, managedsecret): kupe-api's DELETE returns 204 as soon
// as K8s sets `deletionTimestamp`, but the actual teardown — vCluster
// stop, synced K8s Secret cleanup, namespace removal — runs
// asynchronously via finalizers and can take minutes. Without this wait,
// a `terraform destroy` immediately followed by `terraform apply` with
// the same name 409s on "already exists" against the still-terminating
// CR.
//
// getFn should be a closure that calls the resource's GetX client method
// and returns the error verbatim (the value/etag don't matter — we only
// care whether 404 is reached). Callers pass a context with the timeout
// they consider acceptable for that resource type (clusters: 10m,
// secrets: 2m).
//
// Returns nil on success (404 reached), or a wrapped context error on
// timeout. Non-404 errors during polling are tolerated until timeout
// because a transient 5xx shouldn't fail the destroy — but a persistent
// error will surface via the eventual timeout.
func waitForDeleteGone(ctx context.Context, getFn func(context.Context) error) error {
	ticker := time.NewTicker(deletePollInterval)
	defer ticker.Stop()

	// First check immediately; many deletes already complete in under
	// deletePollInterval (small clusters, secrets with no synced
	// targets), so a 2s wait before the first GET is wasted latency.
	if err := getFn(ctx); client.IsNotFound(err) {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for resource to finish terminating: %w", ctx.Err())
		case <-ticker.C:
			if err := getFn(ctx); client.IsNotFound(err) {
				return nil
			}
			// Any non-NotFound error (still terminating, transient
			// 5xx, network) we ignore and keep polling. The ticker
			// bounds how often we re-check; the parent context
			// bounds total time.
		}
	}
}

// apiErrorDetail returns a user-actionable detail string for a kupe-api
// error. The intent is that the user sees not just "kupe api: 401 …" in
// their diagnostic but also a one-sentence pointer at how to fix it. For
// any error that does not unwrap to *client.APIError (network failures,
// JSON decode errors, etc.) the original message is preserved unchanged.
//
// Used by all resources/datasources that call into the API client: the
// summary stays as the verb-driven "failed to {create,read,update,delete}
// {resource}" so diagnostics group sensibly, the detail explains what went
// wrong server-side and what the operator can do about it.
func apiErrorDetail(err error) string {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}

	base := fmt.Sprintf("kupe API returned %d: %s", apiErr.StatusCode, apiErr.Message)
	switch apiErr.StatusCode {
	case http.StatusBadRequest:
		return base + "\n\nThe server rejected the request as malformed. Check the schema of the value you supplied (typically a body_json or routes_json document)."
	case http.StatusUnauthorized:
		return base + "\n\nThe kupe API rejected your credentials. Verify that KUPE_API_KEY (or KUPE_TOKEN) is set and that the value is current for this tenant — API keys can be revoked from the console."
	case http.StatusForbidden:
		return base + "\n\nYour credentials authenticated but lack the role required for this operation. Most mutating operations require the `admin` role on the tenant; reads accept `readonly` as well."
	case http.StatusNotFound:
		return base + "\n\nThe resource does not exist on the server. If you expected it to exist it may have been deleted out-of-band; run `terraform refresh` to reconcile or `terraform import` to re-attach."
	case http.StatusConflict:
		return base + "\n\nThe operation conflicts with current server state. Usually means a resource with this name already exists on the tenant or is being modified concurrently."
	case http.StatusPreconditionFailed:
		return base + "\n\nThe server-side state changed while this apply was running (ETag/If-Match mismatch). Another client modified the same resource concurrently — refresh and re-apply."
	case http.StatusTooManyRequests:
		return base + "\n\nRate limit exceeded. Retry the operation after a short delay; large batch applies may need `-parallelism=1` against the kupe API."
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return base + "\n\nThe kupe API encountered an internal error or is temporarily unavailable. Retry the operation; if the failure persists, check the kupe-api service status."
	default:
		return base
	}
}
