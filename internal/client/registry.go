package client

import (
	"context"
	"errors"
	"net/http"
	"regexp"
)

// Registry is the tenant's container registry in the API response. A tenant
// has one, always named "registry".
type Registry struct {
	Name string `json:"name"`
	// StorageLimit is the tenant's own cap below the plan's ("10Gi"); empty
	// means the plan's cap.
	StorageLimit       string          `json:"storageLimit"`
	DeletionProtection bool            `json:"deletionProtection"`
	Status             *RegistryStatus `json:"status,omitempty"`
	// Generation increments on every spec change; the platform has applied a
	// change once Status.ObservedGeneration catches up with it.
	Generation      int64  `json:"generation"`
	ResourceVersion string `json:"resourceVersion"`
	CreatedAt       string `json:"createdAt"`
}

// RegistryStatus contains registry status fields.
type RegistryStatus struct {
	ObservedGeneration int64  `json:"observedGeneration"`
	Phase              string `json:"phase"`
	// Endpoint is the image path prefix, registry.kupe.cloud/<tenant>.
	Endpoint         string `json:"endpoint"`
	StorageUsedBytes int64  `json:"storageUsedBytes"`
	// StorageLimitBytes is the cap in force, -1 for unlimited; nil until
	// the platform has applied one.
	StorageLimitBytes *int64      `json:"storageLimitBytes,omitempty"`
	RepositoryCount   int64       `json:"repositoryCount"`
	ArtifactCount     int64       `json:"artifactCount"`
	UsageObservedAt   string      `json:"usageObservedAt"`
	Conditions        []Condition `json:"conditions"`
}

// CreateRegistryRequest is the body for creating the registry.
type CreateRegistryRequest struct {
	Name         string `json:"name,omitempty"`
	StorageLimit string `json:"storageLimit,omitempty"`
	// DeletionProtection is a pointer because the API defaults an omitted
	// value to true; an explicit false must reach the wire.
	DeletionProtection *bool `json:"deletionProtection,omitempty"`
}

// PatchRegistryRequest is the JSON merge patch for the registry. Nil fields
// are left unchanged. A StorageLimit of "" removes the tenant's limit, so
// the plan's cap applies.
type PatchRegistryRequest struct {
	StorageLimit       *string `json:"storageLimit,omitempty"`
	DeletionProtection *bool   `json:"deletionProtection,omitempty"`
}

// RegistryBinding is a cluster connection: every pod of the cluster can
// pull from the registry, and each push namespace holds a push credential
// Secret. Credentials are never returned.
type RegistryBinding struct {
	// Name is the cluster's name.
	Name     string `json:"name"`
	Registry string `json:"registry"`
	Cluster  string `json:"cluster"`
	// PushNamespaces are the namespaces that receive a push credential.
	PushNamespaces []string `json:"pushNamespaces"`
	// SecretName is the requested push Secret name; empty means the
	// platform default.
	SecretName      string                 `json:"secretName"`
	Status          *RegistryBindingStatus `json:"status,omitempty"`
	ResourceVersion string                 `json:"resourceVersion"`
	CreatedAt       string                 `json:"createdAt"`
}

// RegistryBindingStatus contains cluster connection status fields.
type RegistryBindingStatus struct {
	// Phase is Ready only when pull works and every push namespace is Ready.
	Phase string `json:"phase"`
	// Message says, once Ready, that pods started earlier need a restart.
	Message        string `json:"message"`
	PullSecretName string `json:"pullSecretName"`
	PullUsername   string `json:"pullUsername"`
	PushUsername   string `json:"pushUsername"`
	// Namespaces is what was delivered into each push namespace. A
	// namespace removed from the connection stays here until withdrawn.
	Namespaces []RegistryBindingNamespaceStatus `json:"namespaces"`
	Conditions []Condition                      `json:"conditions"`
}

// RegistryBindingNamespaceStatus is what a cluster connection delivered
// into one push namespace.
type RegistryBindingNamespaceStatus struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// CreateRegistryBindingRequest is the body for connecting a cluster. The
// registry comes from the request path.
type CreateRegistryBindingRequest struct {
	Cluster        string   `json:"cluster"`
	Name           string   `json:"name,omitempty"`
	PushNamespaces []string `json:"pushNamespaces,omitempty"`
	SecretName     string   `json:"secretName,omitempty"`
}

// PatchRegistryBindingRequest replaces a cluster connection's push
// namespaces, the only field the API can change. PushNamespaces is the
// complete new list; UpdateRegistryBinding sends an empty list as [], which
// removes every push namespace.
type PatchRegistryBindingRequest struct {
	PushNamespaces []string `json:"pushNamespaces"`
}

// RegistryToken is an access token as GET returns it: never its secret.
type RegistryToken struct {
	// ID is rt- and 16 hex characters.
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	// Kind is token (an access token) or session (a login session).
	Kind      string `json:"kind"`
	Access    string `json:"access"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	// Username is the registry username, "" until the token is issued.
	Username string `json:"username"`
	// LastUsedAt is "" until the token is first used.
	LastUsedAt string              `json:"lastUsedAt"`
	Status     RegistryTokenStatus `json:"status"`
}

// RegistryTokenStatus contains access token status fields.
type RegistryTokenStatus struct {
	// Phase is "" or Pending until the token is issued, then Ready; Failed
	// means it can never work.
	Phase      string      `json:"phase"`
	Message    string      `json:"message"`
	Conditions []Condition `json:"conditions"`
}

// CreatedRegistryToken is the create response: the token with its secret,
// which the API returns this once and never again.
type CreatedRegistryToken struct {
	RegistryToken
	// Registry is the registry host to sign in to.
	Registry string `json:"registry"`
	// Secret is the registry password. Never log it.
	Secret string `json:"secret"`
}

// CreateRegistryTokenRequest is the body for creating an access token.
type CreateRegistryTokenRequest struct {
	DisplayName string `json:"displayName"`
	Access      string `json:"access"`
	// ExpiresInDays is nil to take the platform's default (90 days).
	ExpiresInDays *int64 `json:"expiresInDays,omitempty"`
}

// registryNotFoundMessage is how kupe-api words a 404 for a named registry,
// cluster connection, access token or cluster: `registry "registry" not
// found`. A 404 on a registry route without a code and without these words
// comes from an API that has no such route.
var registryNotFoundMessage = regexp.MustCompile(`"[^"]*" not found`)

// RegistriesNotServedError is a 404 from a registry route of a kupe API that
// does not serve registries: it carries no code and does not name what it
// did not find (the HTTP router's "404 page not found"). It says nothing
// about whether a registry exists, so IsNotFound is false for it.
type RegistriesNotServedError struct {
	Err *APIError
}

func (e *RegistriesNotServedError) Error() string {
	return "this kupe API does not serve registries: " + e.Err.Error()
}

func (e *RegistriesNotServedError) Unwrap() error { return e.Err }

// IsRegistriesNotServed reports whether err is a registry route the API
// does not serve.
func IsRegistriesNotServed(err error) bool {
	var notServed *RegistriesNotServedError
	return errors.As(err, &notServed)
}

// IsRegistriesUnavailable reports whether err is the API saying the registry
// is not enabled on the platform. It is a 404, but says nothing about
// whether a registry exists, so IsNotFound is false for it.
func IsRegistriesUnavailable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Code == CodeRegistriesUnavailable
}

// registryError classifies the error of a registry route: a 404 that carries
// no code and names nothing becomes a *RegistriesNotServedError.
func registryError(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Code == "" &&
		!registryNotFoundMessage.MatchString(apiErr.Message) {
		return &RegistriesNotServedError{Err: apiErr}
	}
	return err
}

// registryRequest is requestWithETag for a registry route, its error
// classified by registryError.
func (c *Client) registryRequest(ctx context.Context, method, path, etag string, body, result any) (string, error) {
	newETag, err := c.requestWithETag(ctx, method, path, etag, body, result)
	return newETag, registryError(err)
}

// GetRegistry returns the registry and its ETag.
func (c *Client) GetRegistry(ctx context.Context, name string) (*Registry, string, error) {
	var reg Registry
	etag, err := c.registryRequest(ctx, http.MethodGet, c.tenantPath("registries", name), "", nil, &reg)
	if err != nil {
		return nil, "", err
	}
	return &reg, etag, nil
}

// ListRegistries returns the tenant's registries: at most one.
func (c *Client) ListRegistries(ctx context.Context) ([]Registry, error) {
	var list struct {
		Items []Registry `json:"items"`
	}
	if _, err := c.registryRequest(ctx, http.MethodGet, c.tenantPath("registries"), "", nil, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// CreateRegistry creates the tenant's registry.
func (c *Client) CreateRegistry(ctx context.Context, req CreateRegistryRequest) (*Registry, string, error) {
	var reg Registry
	etag, err := c.registryRequest(ctx, http.MethodPost, c.tenantPath("registries"), "", req, &reg)
	if err != nil {
		return nil, "", err
	}
	return &reg, etag, nil
}

// UpdateRegistry patches the registry with optimistic locking.
func (c *Client) UpdateRegistry(ctx context.Context, name, etag string, req PatchRegistryRequest) (*Registry, string, error) {
	var reg Registry
	newETag, err := c.registryRequest(ctx, http.MethodPatch, c.tenantPath("registries", name), etag, req, &reg)
	if err != nil {
		return nil, "", err
	}
	return &reg, newETag, nil
}

// DeleteRegistry deletes the registry and every image in it. It fails with
// an error matching IsDeletionProtected while deletion protection is on.
func (c *Client) DeleteRegistry(ctx context.Context, name string) error {
	_, err := c.registryRequest(ctx, http.MethodDelete, c.tenantPath("registries", name), "", nil, nil)
	return err
}

// GetRegistryBinding returns a cluster connection of the registry and its
// ETag.
func (c *Client) GetRegistryBinding(ctx context.Context, registry, name string) (*RegistryBinding, string, error) {
	var binding RegistryBinding
	etag, err := c.registryRequest(ctx, http.MethodGet, c.tenantPath("registries", registry, "bindings", name), "", nil, &binding)
	if err != nil {
		return nil, "", err
	}
	return &binding, etag, nil
}

// CreateRegistryBinding connects a cluster to the registry.
func (c *Client) CreateRegistryBinding(ctx context.Context, registry string, req CreateRegistryBindingRequest) (*RegistryBinding, string, error) {
	var binding RegistryBinding
	etag, err := c.registryRequest(ctx, http.MethodPost, c.tenantPath("registries", registry, "bindings"), "", req, &binding)
	if err != nil {
		return nil, "", err
	}
	return &binding, etag, nil
}

// UpdateRegistryBinding replaces a cluster connection's push namespaces with
// optimistic locking. A nil list is sent as [] (the API refuses null).
func (c *Client) UpdateRegistryBinding(ctx context.Context, registry, name, etag string, req PatchRegistryBindingRequest) (*RegistryBinding, string, error) {
	if req.PushNamespaces == nil {
		req.PushNamespaces = []string{}
	}
	var binding RegistryBinding
	newETag, err := c.registryRequest(ctx, http.MethodPatch, c.tenantPath("registries", registry, "bindings", name), etag, req, &binding)
	if err != nil {
		return nil, "", err
	}
	return &binding, newETag, nil
}

// DeleteRegistryBinding disconnects a cluster from the registry.
func (c *Client) DeleteRegistryBinding(ctx context.Context, registry, name string) error {
	_, err := c.registryRequest(ctx, http.MethodDelete, c.tenantPath("registries", registry, "bindings", name), "", nil, nil)
	return err
}

// GetRegistryToken returns an access token, without its secret.
func (c *Client) GetRegistryToken(ctx context.Context, registry, id string) (*RegistryToken, error) {
	var token RegistryToken
	if _, err := c.registryRequest(ctx, http.MethodGet, c.tenantPath("registries", registry, "tokens", id), "", nil, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// CreateRegistryToken creates an access token. The response carries its
// secret, which the API never returns again.
func (c *Client) CreateRegistryToken(ctx context.Context, registry string, req CreateRegistryTokenRequest) (*CreatedRegistryToken, error) {
	var token CreatedRegistryToken
	if _, err := c.registryRequest(ctx, http.MethodPost, c.tenantPath("registries", registry, "tokens"), "", req, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// DeleteRegistryToken revokes an access token.
func (c *Client) DeleteRegistryToken(ctx context.Context, registry, id string) error {
	_, err := c.registryRequest(ctx, http.MethodDelete, c.tenantPath("registries", registry, "tokens", id), "", nil, nil)
	return err
}
