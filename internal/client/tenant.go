package client

import (
	"context"
	"net/http"
	"net/url"
)

// Tenant represents a tenant in the API response.
type Tenant struct {
	Name                string `json:"name"`
	DisplayName         string `json:"displayName"`
	ContactEmail        string `json:"contactEmail"`
	Plan                string `json:"plan"`
	EnforceMetricsLimit bool   `json:"enforceMetricsLimit"`
	EnforceLogLimit     bool   `json:"enforceLogLimit"`
	ResourceVersion     string `json:"resourceVersion"`
	CreatedAt           string `json:"createdAt"`
	Status              any    `json:"status,omitempty"`
	Members             any    `json:"members,omitempty"`
}

// GetTenant returns the tenant's details.
func (c *Client) GetTenant(ctx context.Context) (*Tenant, string, error) {
	var tenant Tenant
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath(), nil, &tenant)
	if err != nil {
		return nil, "", err
	}
	return &tenant, etag, nil
}

// Plan represents a platform plan.
type Plan struct {
	Name              string `json:"name"`
	DisplayName       string `json:"displayName"`
	PlatformFee       string `json:"platformFee"`
	ResourcePool      any    `json:"resourcePool,omitempty"`
	ObservabilityPool any    `json:"observabilityPool,omitempty"`
	MaxClusters       int64  `json:"maxClusters"`
}

// GetPlan returns a platform plan.
func (c *Client) GetPlan(ctx context.Context, name string) (*Plan, error) {
	var plan Plan
	_, err := c.request(ctx, http.MethodGet, "/api/v1/plans/"+url.PathEscape(name), nil, &plan)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}
