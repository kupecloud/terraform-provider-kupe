package provider

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

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
