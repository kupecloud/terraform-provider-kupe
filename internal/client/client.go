// Package client provides an HTTP client for the kupe API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// Client is an HTTP client for the kupe API.
type Client struct {
	baseURL    string
	tenant     string
	httpClient *http.Client
	token      string // Bearer token (API key or OIDC)
	// alertmanagerMu serialises mutating alertmanager operations from
	// this client. The three alertmanager resources (global, receivers,
	// routes) back the same Mimir wrapper object, so a write from one
	// resource invalidates the wrapper ETag stored by another in the
	// same plan. Without this mutex two parallel CRUD goroutines from
	// terraform race their read-modify-write cycles and the loser gets
	// a 412 from the server. See PutAlertmanager* for the refresh+retry
	// inside the lock.
	alertmanagerMu sync.Mutex
}

// retryMax is the number of retry attempts after the initial request for
// transient failures (connection errors, 429, 5xx). Four attempts with
// exponential backoff between retryWaitMin and retryWaitMax covers a brief
// load-balancer/proxy blip or a rolling kupe-api deploy without stalling an
// apply for long. These are vars (not consts) so tests can shrink the
// backoff without changing production behaviour.
var (
	retryMax     = 4
	retryWaitMin = 500 * time.Millisecond
	retryWaitMax = 5 * time.Second
)

// New creates a new kupe API client.
//
// The underlying transport retries transient failures (connection errors,
// HTTP 429, and 5xx) with bounded exponential backoff, honouring any
// Retry-After header, via hashicorp/go-retryablehttp's default policy.
// Non-transient responses (2xx, 4xx other than 429) are returned on the
// first attempt. Mutations stay safe under retry: PATCH carries an
// If-Match ETag (a retried stale write 412s rather than double-applying),
// and create/delete are GET-by-name idempotent or surface a 409 the caller
// already handles.
func New(baseURL, tenant, token string) *Client {
	rc := retryablehttp.NewClient()
	rc.RetryMax = retryMax
	rc.RetryWaitMin = retryWaitMin
	rc.RetryWaitMax = retryWaitMax
	rc.Logger = nil // don't emit retryablehttp's default stderr logging
	// On retry exhaustion, pass the final response straight through instead
	// of wrapping it in retryablehttp's "giving up after N attempts" error.
	// This keeps our >=400 decoding intact so callers still get a typed
	// *APIError (with Code/Field) for a persistent 5xx, and IsNotFound /
	// IsConflict / IsPreconditionFailed keep working after retries.
	rc.ErrorHandler = retryablehttp.PassthroughErrorHandler
	// 30s is a per-attempt ceiling on a single HTTP call. It is distinct
	// from the resource-level `timeouts` blocks (e.g. cluster create 15m),
	// which bound the readiness POLL loop, not an individual request.
	// kupe-api returns immediately (201/202) and provisioning is async, so
	// no single request should approach this. If a future endpoint can
	// legitimately block longer synchronously, derive that call's context
	// deadline from the resource timeout instead of relying on this ceiling.
	rc.HTTPClient.Timeout = 30 * time.Second
	// Do not follow redirects. The default stdlib policy follows up to 10
	// redirects and would replay the request body (alertmanager secrets on
	// PUT) and the Authorization bearer header to whatever host the redirect
	// points at. We constrain the base host in normalizeHost; refusing
	// redirects here closes the replay-to-unintended-host surface entirely.
	// ErrUseLastResponse makes Do return the 3xx response as-is, which
	// requestWithETag's non-2xx handling surfaces to the user as an error
	// rather than silently accepting the redirect as a successful write.
	noRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	rc.HTTPClient.CheckRedirect = noRedirect

	// StandardClient() wraps the retrying transport in a fresh *http.Client
	// whose own redirect policy defaults to "follow". Without setting it, a
	// 3xx returned by the transport would be followed by THIS outer client
	// (replaying the request to the redirect target and, if that target
	// answers 2xx, masking a never-processed mutation as success). Refuse
	// redirects on the outer client too so the 3xx reaches requestWithETag.
	std := rc.StandardClient()
	std.CheckRedirect = noRedirect

	return &Client{
		baseURL:    baseURL,
		tenant:     tenant,
		token:      token,
		httpClient: std,
	}
}

// ErrorResponse is the standard error JSON from the API.
type ErrorResponse struct {
	Error string `json:"error"`
}

// errorEnvelope decodes both the legacy ({"error":"..."}) and the structured
// canonical ({"code":"...","severity":"...","message":"...","field":"...",
// "error":"..."}) shapes from kupe-api in a single pass. The structured
// shape duplicates `error` for backward compatibility, so legacy-only
// callers still see a useful Message.
type errorEnvelope struct {
	Code     string `json:"code,omitempty"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message,omitempty"`
	Field    string `json:"field,omitempty"`
	Error    string `json:"error,omitempty"`
}

// APIError represents an error from the kupe API.
//
// Code/Severity/Field are populated only when the response is the
// structured canonical envelope (HA_DISABLE_UNSUPPORTED,
// CLUSTER_DEDICATED_UNSUPPORTED, etc.). For legacy {"error":"..."} or
// unparsable bodies they're empty and only Message is filled.
type APIError struct {
	StatusCode int
	Message    string
	// Code is the canonical error code (e.g. "HA_DISABLE_UNSUPPORTED").
	Code string
	// Severity is "error" or "warning"; empty when Code is empty.
	Severity string
	// Field is the dotted spec path the error applies to.
	Field string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kupe api: %d %s", e.StatusCode, e.Message)
}

// IsNotFound returns true if the error is a 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// IsConflict returns true if the error is a 409.
func IsConflict(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusConflict
	}
	return false
}

// IsPreconditionFailed returns true if the error is a 412 (stale If-Match).
// The alertmanager Put paths use this to detect a stale wrapper ETag and
// drive the bounded refresh+retry loop in putAlertmanagerWithRetry.
func IsPreconditionFailed(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusPreconditionFailed
	}
	return false
}

// request executes an HTTP request and decodes the JSON response.
func (c *Client) request(ctx context.Context, method, path string, body, result any) (string, error) {
	return c.requestWithETag(ctx, method, path, "", body, result)
}

// requestWithETag executes an HTTP request with optional If-Match header.
// Returns the ETag from the response.
func (c *Client) requestWithETag(ctx context.Context, method, path, etag string, body, result any) (string, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return "", fmt.Errorf("marshalling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "terraform-provider-kupe")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if etag != "" {
		req.Header.Set("If-Match", etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB limit
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	// Only a 2xx is a success. Anything else — including a 3xx the client
	// was handed because CheckRedirect refuses to follow redirects — is an
	// error. Treating a 3xx as success would let a DELETE/PUT/POST against a
	// redirecting endpoint (trailing-slash normalisation, http→https on a
	// misconfigured host, a proxy/ingress redirect) "succeed" while the
	// server never processed the mutation, dropping the resource from state
	// or writing a zero-valued object (orphaned billable clusters).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: resp.StatusCode, Message: string(respBody)}
		// Single decode handles both shapes: structured envelopes carry
		// Code (canonical) while legacy responses only carry Error. If the
		// body isn't valid JSON we keep the raw string as Message so the
		// user still sees something useful (e.g. proxy/HTML error pages).
		var env errorEnvelope
		if json.Unmarshal(respBody, &env) == nil {
			switch {
			case env.Code != "":
				apiErr.Code = env.Code
				apiErr.Severity = env.Severity
				apiErr.Field = env.Field
				switch {
				case env.Message != "":
					apiErr.Message = env.Message
				case env.Error != "":
					apiErr.Message = env.Error
				}
			case env.Error != "":
				apiErr.Message = env.Error
			case env.Message != "":
				apiErr.Message = env.Message
			}
		}
		// A redirect (or any status with an empty/non-JSON body) leaves
		// Message blank; fall back to the HTTP status text so the user sees
		// e.g. "301 Moved Permanently" instead of an empty error.
		if apiErr.Message == "" {
			apiErr.Message = http.StatusText(resp.StatusCode)
		}
		return "", apiErr
	}

	responseETag := resp.Header.Get("ETag")

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return "", fmt.Errorf("decoding response: %w", err)
		}
	}

	return responseETag, nil
}

// tenantPath builds a tenant-scoped API path with proper URL escaping.
func (c *Client) tenantPath(segments ...string) string {
	path := "/api/v1/tenants/" + url.PathEscape(c.tenant)
	for _, s := range segments {
		path += "/" + url.PathEscape(s)
	}
	return path
}
