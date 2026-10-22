package client

import (
	"context"
	"errors"
	"net/http"
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
	// Conditions carry the reason behind the phase. Ready is False while
	// a change rolls through the instances, though the phase stays Running.
	Conditions []Condition `json:"conditions"`
}

// Ready reports whether the database's Ready condition is True.
func (s *DatabaseStatus) Ready() bool {
	return s != nil && ConditionTrue(s.Conditions, ConditionReady)
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

// DatabaseBinding grants one role of a database to namespaces of one
// cluster.
type DatabaseBinding struct {
	Name     string `json:"name"`
	Database string `json:"database"`
	Cluster  string `json:"cluster"`
	// Namespaces are the namespaces the binding delivers into.
	Namespaces []string `json:"namespaces"`
	Role       string   `json:"role"`
	// SecretName is the requested name; empty means the API default.
	SecretName      string                 `json:"secretName"`
	Status          *DatabaseBindingStatus `json:"status,omitempty"`
	ResourceVersion string                 `json:"resourceVersion"`
	CreatedAt       string                 `json:"createdAt"`
}

// DatabaseBindingStatus contains binding status fields.
type DatabaseBindingStatus struct {
	// Phase is Ready only when every listed namespace is Ready.
	Phase string `json:"phase"`
	// SecretName is the effective credentials Secret name in every listed
	// namespace.
	SecretName string `json:"secretName"`
	Message    string `json:"message"`
	// Namespaces is what was delivered into each namespace. A namespace
	// removed from the binding stays here until it is withdrawn.
	Namespaces []DatabaseBindingNamespaceStatus `json:"namespaces"`
	// Conditions carry the reason behind the phase, e.g. Conflict True
	// (reason SyncConflict) on a Failed binding.
	Conditions []Condition `json:"conditions"`
}

// DatabaseBindingNamespaceStatus is what a binding delivered into one
// namespace.
type DatabaseBindingNamespaceStatus struct {
	Name string `json:"name"`
	// Host is the in-cluster DNS name applications in the namespace connect
	// to; empty while nothing is delivered.
	Host    string `json:"host"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// Condition is one status condition of a resource.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// ConditionReady is the condition that is True once a cluster or database
// runs with its latest change in effect.
const ConditionReady = "Ready"

// ReasonReconcileFailed is the reason of a cluster's or database's Ready
// condition while the platform fails to apply its spec and retries. The
// platform does not mark a spec it failed to apply as observed.
const ReasonReconcileFailed = "ReconcileFailed"

// ConditionTrue reports whether the condition of type t is True.
func ConditionTrue(conditions []Condition, t string) bool {
	return findCondition(conditions, t).Status == "True"
}

// ConditionMessage is the message of the condition of type t, "" when
// there is none.
func ConditionMessage(conditions []Condition, t string) string {
	return findCondition(conditions, t).Message
}

// ConditionReason is the reason of the condition of type t, "" when there
// is none.
func ConditionReason(conditions []Condition, t string) string {
	return findCondition(conditions, t).Reason
}

// findCondition is the condition of type t, the zero Condition when there
// is none.
func findCondition(conditions []Condition, t string) Condition {
	for _, c := range conditions {
		if c.Type == t {
			return c
		}
	}
	return Condition{}
}

// CreateDatabaseBindingRequest is the body for creating a binding. The
// database comes from the request path.
type CreateDatabaseBindingRequest struct {
	Name       string   `json:"name"`
	Cluster    string   `json:"cluster"`
	Namespaces []string `json:"namespaces"`
	Role       string   `json:"role"`
	SecretName string   `json:"secretName,omitempty"`
}

// PatchDatabaseBindingRequest changes a binding's namespaces, the only
// field the API can change. Namespaces is the complete new list.
type PatchDatabaseBindingRequest struct {
	Namespaces []string `json:"namespaces"`
}

// IsDeletionProtected reports whether err is the API refusing a database or
// registry delete because deletion protection is enabled.
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

// GetDatabaseBinding returns a single binding of a database and its ETag.
func (c *Client) GetDatabaseBinding(ctx context.Context, database, name string) (*DatabaseBinding, string, error) {
	var binding DatabaseBinding
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("databases", database, "bindings", name), nil, &binding)
	if err != nil {
		return nil, "", err
	}
	return &binding, etag, nil
}

// CreateDatabaseBinding binds a database into namespaces of a cluster.
func (c *Client) CreateDatabaseBinding(ctx context.Context, database string, req CreateDatabaseBindingRequest) (*DatabaseBinding, string, error) {
	var binding DatabaseBinding
	etag, err := c.request(ctx, http.MethodPost, c.tenantPath("databases", database, "bindings"), req, &binding)
	if err != nil {
		return nil, "", err
	}
	return &binding, etag, nil
}

// UpdateDatabaseBinding replaces a binding's namespaces with optimistic
// locking.
func (c *Client) UpdateDatabaseBinding(ctx context.Context, database, name, etag string, req PatchDatabaseBindingRequest) (*DatabaseBinding, string, error) {
	var binding DatabaseBinding
	newETag, err := c.requestWithETag(ctx, http.MethodPatch, c.tenantPath("databases", database, "bindings", name), etag, req, &binding)
	if err != nil {
		return nil, "", err
	}
	return &binding, newETag, nil
}

// DeleteDatabaseBinding removes a binding.
func (c *Client) DeleteDatabaseBinding(ctx context.Context, database, name string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("databases", database, "bindings", name), nil, nil)
	return err
}
