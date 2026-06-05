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

// New creates a new kupe API client.
func New(baseURL, tenant, token string) *Client {
	return &Client{
		baseURL: baseURL,
		tenant:  tenant,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
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

	if resp.StatusCode >= 400 {
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
