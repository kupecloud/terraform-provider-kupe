package client

import (
	"context"
	"errors"
	"net/http"
)

// ClusterAccessTokensNotServedError is a 404 from an access token route of a
// kupe API that does not serve cluster access tokens (one older than them,
// or a misrouted ingress): it carries no code and does not name what it did
// not find. It says nothing about whether a token exists, so IsNotFound is
// false for it: refresh keeps the token in state, and delete does not report
// a revoke that never happened. kupe-api's own 404 for a token or its
// cluster names it (`access token "cat-..." not found`, `cluster "dev" not
// found`), as registryNotFoundMessage matches.
type ClusterAccessTokensNotServedError struct {
	Err *APIError
}

func (e *ClusterAccessTokensNotServedError) Error() string {
	return "this kupe API does not serve cluster access tokens: " + e.Err.Error()
}

func (e *ClusterAccessTokensNotServedError) Unwrap() error { return e.Err }

// IsClusterAccessTokensNotServed reports whether err is an access token
// route the API does not serve.
func IsClusterAccessTokensNotServed(err error) bool {
	var notServed *ClusterAccessTokensNotServedError
	return errors.As(err, &notServed)
}

// clusterAccessTokenRequest is request for an access token route, a 404 that
// carries no code and names nothing classified as
// *ClusterAccessTokensNotServedError.
func (c *Client) clusterAccessTokenRequest(ctx context.Context, method, path string, body, result any) error {
	_, err := c.request(ctx, method, path, body, result)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Code == "" &&
		!registryNotFoundMessage.MatchString(apiErr.Message) {
		return &ClusterAccessTokensNotServedError{Err: apiErr}
	}
	return err
}

// ClusterAccess is what a cluster access token may do: v1 offers the admin
// role only.
type ClusterAccess struct {
	Role string `json:"role"`
}

// ClusterAccessToken is a cluster access token as GET returns it: never the
// token itself.
type ClusterAccessToken struct {
	// ID is cat- and 16 hex characters.
	ID          string        `json:"id"`
	Cluster     string        `json:"cluster"`
	DisplayName string        `json:"displayName"`
	Access      ClusterAccess `json:"access"`
	// CreatedBy is the creator's email, or ak-<id> for an API key.
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	// CollectedAt is "" until the token is collected.
	CollectedAt string                   `json:"collectedAt"`
	Collected   bool                     `json:"collected"`
	Status      ClusterAccessTokenStatus `json:"status"`
}

// ClusterAccessTokenStatus contains cluster access token status fields.
type ClusterAccessTokenStatus struct {
	// Phase is Pending, Ready, Failed or Revoking.
	Phase      string      `json:"phase"`
	Message    string      `json:"message"`
	Conditions []Condition `json:"conditions"`
}

// CreateClusterAccessTokenRequest is the body for creating a cluster access
// token.
type CreateClusterAccessTokenRequest struct {
	DisplayName string        `json:"displayName"`
	Access      ClusterAccess `json:"access"`
	// ExpiresInDays is nil to take the platform's default (90 days).
	ExpiresInDays *int64 `json:"expiresInDays,omitempty"`
}

// CollectedClusterAccessToken is the one-time collect response. Never log
// Token or Kubeconfig.
type CollectedClusterAccessToken struct {
	ID         string `json:"id"`
	Cluster    string `json:"cluster"`
	Token      string `json:"token"`
	Kubeconfig string `json:"kubeconfig"`
	Endpoint   string `json:"endpoint"`
	// CertificateAuthority is the cluster's CA, base64-encoded PEM.
	CertificateAuthority string `json:"certificateAuthority"`
	ExpiresAt            string `json:"expiresAt"`
}

// GetClusterAccessToken returns a cluster access token, without the token.
func (c *Client) GetClusterAccessToken(ctx context.Context, cluster, id string) (*ClusterAccessToken, error) {
	var token ClusterAccessToken
	if err := c.clusterAccessTokenRequest(ctx, http.MethodGet, c.tenantPath("clusters", cluster, "access-tokens", id), nil, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// CreateClusterAccessToken creates a cluster access token. The response
// carries no token: collect it once it is Ready.
func (c *Client) CreateClusterAccessToken(ctx context.Context, cluster string, req CreateClusterAccessTokenRequest) (*ClusterAccessToken, error) {
	var token ClusterAccessToken
	if err := c.clusterAccessTokenRequest(ctx, http.MethodPost, c.tenantPath("clusters", cluster, "access-tokens"), req, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// CollectClusterAccessToken collects the token and its kubeconfig. The API
// serves them once, to the token's creator; it is a POST, so the transport
// never retries it.
func (c *Client) CollectClusterAccessToken(ctx context.Context, cluster, id string) (*CollectedClusterAccessToken, error) {
	var collected CollectedClusterAccessToken
	if err := c.clusterAccessTokenRequest(ctx, http.MethodPost, c.tenantPath("clusters", cluster, "access-tokens", id, "token"), nil, &collected); err != nil {
		return nil, err
	}
	return &collected, nil
}

// DeleteClusterAccessToken revokes a cluster access token.
func (c *Client) DeleteClusterAccessToken(ctx context.Context, cluster, id string) error {
	return c.clusterAccessTokenRequest(ctx, http.MethodDelete, c.tenantPath("clusters", cluster, "access-tokens", id), nil, nil)
}
