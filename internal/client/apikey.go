package client

import (
	"context"
	"net/http"
)

// APIKey represents an API key in the API response.
type APIKey struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	CreatedBy   string `json:"createdBy"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	LastUsedAt  string `json:"lastUsedAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
	Key         string `json:"key,omitempty"` // Only returned on creation
}

// CreateAPIKeyRequest is the body for creating an API key.
type CreateAPIKeyRequest struct {
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

// ListAPIKeys lists all API keys for the tenant.
func (c *Client) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	var resp struct {
		Items []APIKey `json:"items"`
	}
	_, err := c.request(ctx, http.MethodGet, c.tenantPath("apikeys"), nil, &resp)
	return resp.Items, err
}

// CreateAPIKey creates a new API key. The raw key is only returned once.
func (c *Client) CreateAPIKey(ctx context.Context, req CreateAPIKeyRequest) (*APIKey, error) {
	var apiKey APIKey
	_, err := c.request(ctx, http.MethodPost, c.tenantPath("apikeys"), req, &apiKey)
	if err != nil {
		return nil, err
	}
	return &apiKey, nil
}

// DeleteAPIKey revokes an API key.
func (c *Client) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("apikeys", id), nil, nil)
	return err
}
