package client

import (
	"context"
	"net/http"
)

// Secret represents a managed secret in the API response.
type Secret struct {
	Name            string        `json:"name"`
	SecretPath      string        `json:"secretPath"`
	Sync            []SyncTarget  `json:"sync,omitempty"`
	Status          *SecretStatus `json:"status,omitempty"`
	ResourceVersion string        `json:"resourceVersion"`
	CreatedAt       string        `json:"createdAt"`
}

// SyncTarget defines a cluster/namespace to sync a secret to.
type SyncTarget struct {
	Cluster    string `json:"cluster"`
	Namespace  string `json:"namespace"`
	SecretName string `json:"secretName,omitempty"`
}

// SecretStatus contains secret status fields.
type SecretStatus struct {
	Phase string `json:"phase"`
}

// CreateSecretRequest is the body for creating a secret. `sync` is sent
// unconditionally (no `omitempty`) so it matches the Patch contract:
// an empty list explicitly clears sync targets, a null value would mean
// "no sync configured." Without this, Create with `sync = []` would
// silently drop the field — confusing when paired with the Update path.
type CreateSecretRequest struct {
	Name       string       `json:"name"`
	SecretPath string       `json:"secretPath"`
	Sync       []SyncTarget `json:"sync"`
}

// PatchSecretRequest is the body for updating a secret's sync targets.
// `sync` is intentionally not `omitempty` — sending an empty array is
// the documented way to clear all sync targets for the secret.
type PatchSecretRequest struct {
	Sync []SyncTarget `json:"sync"`
}

// GetSecret returns a single managed secret.
func (c *Client) GetSecret(ctx context.Context, name string) (*Secret, string, error) {
	var secret Secret
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("secrets", name), nil, &secret)
	if err != nil {
		return nil, "", err
	}
	return &secret, etag, nil
}

// CreateSecret creates a new managed secret.
func (c *Client) CreateSecret(ctx context.Context, req CreateSecretRequest) (*Secret, string, error) {
	var secret Secret
	etag, err := c.request(ctx, http.MethodPost, c.tenantPath("secrets"), req, &secret)
	if err != nil {
		return nil, "", err
	}
	return &secret, etag, nil
}

// UpdateSecret patches a secret's sync targets.
func (c *Client) UpdateSecret(ctx context.Context, name, etag string, req PatchSecretRequest) (*Secret, string, error) {
	var secret Secret
	newETag, err := c.requestWithETag(ctx, http.MethodPatch, c.tenantPath("secrets", name), etag, req, &secret)
	if err != nil {
		return nil, "", err
	}
	return &secret, newETag, nil
}

// DeleteSecret deletes a managed secret.
func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("secrets", name), nil, nil)
	return err
}
