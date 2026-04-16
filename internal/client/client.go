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
	"time"
)

// Client is an HTTP client for the kupe API.
type Client struct {
	baseURL    string
	tenant     string
	httpClient *http.Client
	token      string // Bearer token (API key or OIDC)
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

// APIError represents an error from the kupe API.
type APIError struct {
	StatusCode int
	Message    string
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
			return "", fmt.Errorf("marshaling request body: %w", err)
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
		var errResp ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return "", &APIError{StatusCode: resp.StatusCode, Message: errResp.Error}
		}
		return "", &APIError{StatusCode: resp.StatusCode, Message: string(respBody)}
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
