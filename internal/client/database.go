package client

import (
	"context"
	"errors"
	"net/http"
)

// Error codes kupe-api returns for managed databases.
const (
	// CodeDeletionProtected comes with a 409 when a database delete is
	// refused because deletion protection is enabled.
	CodeDeletionProtected = "deletion_protected"
	// CodeDatabasesUnavailable comes with a 404 from every database route
	// when managed databases are not enabled on the platform.
	CodeDatabasesUnavailable = "databases_unavailable"
)

// Database is a managed database in the API response. The API never returns
// credentials: they reach applications only through bindings.
type Database struct {
	Name               string              `json:"name"`
	Engine             string              `json:"engine"`
	Version            string              `json:"version"`
	Resources          *ResourceQuantities `json:"resources,omitempty"`
	HighAvailability   bool                `json:"highAvailability"`
	DeletionProtection bool                `json:"deletionProtection"`
	Status             *DatabaseStatus     `json:"status,omitempty"`
	// Generation increments on every spec change; the operator has applied
	// a change once Status.ObservedGeneration catches up with it.
	Generation      int64  `json:"generation"`
	ResourceVersion string `json:"resourceVersion"`
	CreatedAt       string `json:"createdAt"`
}

// DatabaseStatus contains database status fields.
type DatabaseStatus struct {
	ObservedGeneration int64  `json:"observedGeneration"`
	Phase              string `json:"phase"`
	// CurrentVersion is the full engine version running, e.g. "18.0".
	CurrentVersion string `json:"currentVersion"`
}

// CreateDatabaseRequest is the body for creating a database.
type CreateDatabaseRequest struct {
	Name             string              `json:"name"`
	Engine           string              `json:"engine,omitempty"`
	Version          string              `json:"version,omitempty"`
	Resources        *ResourceQuantities `json:"resources"`
	HighAvailability bool                `json:"highAvailability,omitempty"`
	// DeletionProtection is a pointer because the API defaults an omitted
	// value to true; an explicit false must reach the wire.
	DeletionProtection *bool `json:"deletionProtection,omitempty"`
}

// PatchDatabaseRequest is the JSON merge patch for a database. Nil fields
// are left unchanged; these four are the only fields the API can change.
type PatchDatabaseRequest struct {
	Version            *string             `json:"version,omitempty"`
	Resources          *ResourceQuantities `json:"resources,omitempty"`
	HighAvailability   *bool               `json:"highAvailability,omitempty"`
	DeletionProtection *bool               `json:"deletionProtection,omitempty"`
}

// DatabaseBinding grants one role of a database to one namespace of one
// cluster.
type DatabaseBinding struct {
	Name      string `json:"name"`
	Database  string `json:"database"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Role      string `json:"role"`
	// SecretName is the requested name; empty means the API default.
	SecretName      string                 `json:"secretName"`
	Status          *DatabaseBindingStatus `json:"status,omitempty"`
	ResourceVersion string                 `json:"resourceVersion"`
	CreatedAt       string                 `json:"createdAt"`
}

// DatabaseBindingStatus contains binding status fields.
type DatabaseBindingStatus struct {
	Phase string `json:"phase"`
	// Host is the in-cluster DNS name applications connect to.
	Host string `json:"host"`
	// SecretName is the effective credentials Secret name in the namespace.
	SecretName string `json:"secretName"`
	Message    string `json:"message"`
}

// CreateDatabaseBindingRequest is the body for creating a binding. The
// database comes from the request path.
type CreateDatabaseBindingRequest struct {
	Name       string `json:"name"`
	Cluster    string `json:"cluster"`
	Namespace  string `json:"namespace"`
	Role       string `json:"role"`
	SecretName string `json:"secretName,omitempty"`
}

// IsDeletionProtected reports whether err is the API refusing a database
// delete because deletion protection is enabled.
func IsDeletionProtected(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.Code == CodeDeletionProtected
}

// IsDatabasesUnavailable reports whether err is the API saying managed
// databases are not enabled. It is a 404, but says nothing about whether a
// database exists, so IsNotFound is false for it.
func IsDatabasesUnavailable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Code == CodeDatabasesUnavailable
}

// GetDatabase returns a single database.
func (c *Client) GetDatabase(ctx context.Context, name string) (*Database, string, error) {
	var db Database
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("databases", name), nil, &db)
	if err != nil {
		return nil, "", err
	}
	return &db, etag, nil
}

// CreateDatabase creates a new database.
func (c *Client) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) (*Database, string, error) {
	var db Database
	etag, err := c.request(ctx, http.MethodPost, c.tenantPath("databases"), req, &db)
	if err != nil {
		return nil, "", err
	}
	return &db, etag, nil
}

// UpdateDatabase patches a database with optimistic locking.
func (c *Client) UpdateDatabase(ctx context.Context, name, etag string, req PatchDatabaseRequest) (*Database, string, error) {
	var db Database
	newETag, err := c.requestWithETag(ctx, http.MethodPatch, c.tenantPath("databases", name), etag, req, &db)
	if err != nil {
		return nil, "", err
	}
	return &db, newETag, nil
}

// DeleteDatabase deletes a database. It fails with an error matching
// IsDeletionProtected while deletion protection is enabled.
func (c *Client) DeleteDatabase(ctx context.Context, name string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("databases", name), nil, nil)
	return err
}

// GetDatabaseBinding returns a single binding of a database.
func (c *Client) GetDatabaseBinding(ctx context.Context, database, name string) (*DatabaseBinding, error) {
	var binding DatabaseBinding
	if _, err := c.request(ctx, http.MethodGet, c.tenantPath("databases", database, "bindings", name), nil, &binding); err != nil {
		return nil, err
	}
	return &binding, nil
}

// CreateDatabaseBinding binds a database into a cluster namespace.
func (c *Client) CreateDatabaseBinding(ctx context.Context, database string, req CreateDatabaseBindingRequest) (*DatabaseBinding, error) {
	var binding DatabaseBinding
	if _, err := c.request(ctx, http.MethodPost, c.tenantPath("databases", database, "bindings"), req, &binding); err != nil {
		return nil, err
	}
	return &binding, nil
}

// DeleteDatabaseBinding removes a binding.
func (c *Client) DeleteDatabaseBinding(ctx context.Context, database, name string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("databases", database, "bindings", name), nil, nil)
	return err
}
