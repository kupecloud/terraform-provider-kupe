package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

// rbacForbiddenMessage is the verbatim message kupe-api returns for true
// RBAC denials (role lacks permission for this operation). Admission
// webhook denials surface via a different status code (400) and never
// match this string, so we use it as a discriminator: if a 403 carries
// this exact message we add the role-hint, otherwise we don't, so the
// user is not misled into thinking their API key is wrong when the API
// is really telling them e.g. "unsupported Kubernetes version".
const rbacForbiddenMessage = "access denied"

// defaultPollInterval is how often waitForCondition re-checks the server.
// 2s gives reasonable feedback latency without hammering kupe-api during
// a multi-minute teardown or provisioning operation. Tunable as a var so
// tests can shrink it without poking the helper itself.
var defaultPollInterval = 2 * time.Second

// waitForCondition polls fn at defaultPollInterval until it reports the
// terminal state (done=true) or the context expires.
//
//   - fn returns (true, nil): success — caller exits with nil
//   - fn returns (true, err): terminal failure observed — caller surfaces
//     err. Not currently used (we treat Degraded as in-progress), but the
//     signature supports a future hard-failure detection path
//   - fn returns (false, _): keep polling. Any transient error inside the
//     closure is the caller's responsibility to swallow; the helper
//     doesn't inspect it
//
// On timeout the helper returns a wrapped context error so callers can
// `errors.Is(err, context.DeadlineExceeded)` and decide whether to fail
// or warn. The caller passes a context with whatever timeout is
// appropriate for the resource type (cluster create: 15m via the
// user-overridable timeouts block; secret create: 2m; etc.).
//
// fn is also responsible for capturing observed state — typically by
// writing into the framework's `resp.State` from the surrounding
// handler — so an interrupted apply leaves Terraform with the latest
// values, not stale ones.
//
// Used by Create/Update (predicate: phase reached the Ready value) and
// Delete (predicate: GET returns IsNotFound).
func waitForCondition(ctx context.Context, fn func(context.Context) (bool, error)) error {
	// First check immediately; many fast operations complete before the
	// first ticker fires (creates that go straight to Running on a
	// warm cluster, deletes with no synced state to clean up).
	if done, err := fn(ctx); done {
		return err
	}

	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for resource to converge: %w", ctx.Err())
		case <-ticker.C:
			if done, err := fn(ctx); done {
				return err
			}
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
		// 400 covers two server-side outcomes that surface here:
		// (a) the request body failed schema decoding (typo in
		//     body_json / routes_json, missing required field), and
		// (b) an admission webhook on the platform rejected the
		//     request as semantically invalid (e.g. unsupported
		//     Kubernetes version, immutable field changed). In both
		//     cases the verbatim API message is the actionable signal;
		//     adding a schema-focused hint here would mislead users
		//     hitting case (b). Surface the message as-is.
		return base
	case http.StatusUnauthorized:
		return base + "\n\nThe kupe API rejected your credentials. Verify that KUPE_API_KEY (or KUPE_TOKEN) is set and that the value is current for this tenant — API keys can be revoked from the console."
	case http.StatusForbidden:
		// Only show the role hint for true RBAC denials. The kupe-api
		// returns "access denied" as the verbatim message for those;
		// any other 403 message we receive is from an older API that
		// still routed webhook denials through 403 (newer API uses
		// 400). For non-RBAC bodies the role hint is misleading.
		if strings.EqualFold(strings.TrimSpace(apiErr.Message), rbacForbiddenMessage) {
			return base + "\n\nYour credentials authenticated but lack the role required for this operation. Most mutating operations require the `admin` role on the tenant; reads accept `readonly` as well."
		}
		return base
	case http.StatusNotFound:
		return base + "\n\nThe resource does not exist on the server. If you expected it to exist it may have been deleted out-of-band; run `terraform refresh` to reconcile or `terraform import` to re-attach."
	case http.StatusConflict:
		return base + "\n\nThe operation conflicts with current server state. Usually means a resource with this name already exists on the tenant or is being modified concurrently."
	case http.StatusPreconditionFailed:
		return base + "\n\nThe server-side state changed while this apply was running (ETag/If-Match mismatch). Another client modified the same resource concurrently — refresh and re-apply."
	case http.StatusTooManyRequests:
		return base + "\n\nRate limit exceeded. Retry the operation after a short delay; large batch applies may need `-parallelism=1` against the kupe API."
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return base + "\n\nThe Kupe Cloud API encountered an internal error or is temporarily unavailable. Retry the operation after a short delay; if the failure persists, check the status page or contact Kupe Cloud support."
	default:
		return base
	}
}
