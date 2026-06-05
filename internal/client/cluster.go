package client

import (
	"context"
	"net/http"
)

// Cluster represents a managed cluster in the API response.
type Cluster struct {
	Name             string           `json:"name"`
	DisplayName      string           `json:"displayName"`
	Type             string           `json:"type"`
	Version          string           `json:"version"`
	Resources        *ClusterResource `json:"resources,omitempty"`
	Alerts           any              `json:"alerts,omitempty"`
	HighAvailability bool             `json:"highAvailability,omitempty"`
	Status           *ClusterStatus   `json:"status,omitempty"`
	ResourceVersion  string           `json:"resourceVersion"`
	CreatedAt        string           `json:"createdAt"`
	// Warnings is advisory; today populated only on POST /clusters
	// responses (e.g. HA_K8S_VERSION_RETIRING). Receive-only — no
	// omitempty so the decoder happily accepts the always-present empty
	// array without dropping the field.
	Warnings []Warning `json:"warnings"`
}

// Warning is one advisory entry. Same shape as the structured error
// envelope minus the duplicated `error` compatibility field.
type Warning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Field    string `json:"field,omitempty"`
}

// ClusterResource defines resource limits.
type ClusterResource struct {
	CPU     string `json:"cpu,omitempty"`
	Memory  string `json:"memory,omitempty"`
	Storage string `json:"storage,omitempty"`
}

// ClusterStatus contains cluster status fields.
type ClusterStatus struct {
	Phase             string `json:"phase"`
	KubernetesVersion string `json:"kubernetesVersion"`
	Endpoint          string `json:"endpoint"`
	// HAConfigured flips true once the operator confirms both 3/3 apiserver replicas
	// AND 3/3 deployed-etcd replicas are Ready. Etcd readiness is required because
	// the OSS deployed-etcd path runs etcd in its own StatefulSet — quorum loss with
	// healthy apiserver pods still blocks writes. HAEnabledAt is the billing anchor
	// (stamped once, never updated).
	HAConfigured bool   `json:"haConfigured,omitempty"`
	HAEnabledAt  string `json:"haEnabledAt,omitempty"`
	// HAPhase is the consumer-friendly HA rollup (pending, ha-healthy,
	// ha-degraded, ha-unavailable). Empty for non-HA clusters.
	HAPhase string `json:"haPhase,omitempty"`
	// HAReplicasReady / HAReplicasDesired surface the apiserver "N of M"
	// pair so downstream HCL can render meaningful status without parsing
	// conditions.
	HAReplicasReady   int32 `json:"haReplicasReady,omitempty"`
	HAReplicasDesired int32 `json:"haReplicasDesired,omitempty"`
	// HAEtcdReplicasReady / HAEtcdReplicasDesired surface the deployed-etcd
	// "N of M" pair separately. In the OSS deployed-etcd path etcd is its
	// own StatefulSet — a 3/3 CP with a 2/3 etcd is still degraded because
	// etcd quorum loss blocks writes.
	HAEtcdReplicasReady   int32 `json:"haEtcdReplicasReady,omitempty"`
	HAEtcdReplicasDesired int32 `json:"haEtcdReplicasDesired,omitempty"`
}

// CreateClusterRequest is the body for creating a cluster.
type CreateClusterRequest struct {
	Name             string           `json:"name"`
	DisplayName      string           `json:"displayName"`
	Type             string           `json:"type"`
	Version          string           `json:"version,omitempty"`
	Resources        *ClusterResource `json:"resources,omitempty"`
	Alerts           any              `json:"alerts,omitempty"`
	HighAvailability bool             `json:"highAvailability,omitempty"`
}

// PatchClusterRequest is the body for updating a cluster. HighAvailability
// uses *bool so the provider can distinguish "no change" (nil) from "set to
// false" (the operator rejects true → false in v1).
type PatchClusterRequest struct {
	Version          *string          `json:"version,omitempty"`
	Resources        *ClusterResource `json:"resources,omitempty"`
	Alerts           any              `json:"alerts,omitempty"`
	HighAvailability *bool            `json:"highAvailability,omitempty"`
}

// ListClusters lists all clusters for the tenant.
func (c *Client) ListClusters(ctx context.Context) ([]Cluster, error) {
	var resp struct {
		Items []Cluster `json:"items"`
	}
	_, err := c.request(ctx, http.MethodGet, c.tenantPath("clusters"), nil, &resp)
	return resp.Items, err
}

// GetCluster returns a single cluster.
func (c *Client) GetCluster(ctx context.Context, name string) (*Cluster, string, error) {
	var cluster Cluster
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("clusters", name), nil, &cluster)
	if err != nil {
		return nil, "", err
	}
	return &cluster, etag, nil
}

// CreateCluster creates a new cluster.
func (c *Client) CreateCluster(ctx context.Context, req CreateClusterRequest) (*Cluster, string, error) {
	var cluster Cluster
	etag, err := c.request(ctx, http.MethodPost, c.tenantPath("clusters"), req, &cluster)
	if err != nil {
		return nil, "", err
	}
	return &cluster, etag, nil
}

// UpdateCluster patches a cluster with optimistic locking.
func (c *Client) UpdateCluster(ctx context.Context, name, etag string, req PatchClusterRequest) (*Cluster, string, error) {
	var cluster Cluster
	newETag, err := c.requestWithETag(ctx, http.MethodPatch, c.tenantPath("clusters", name), etag, req, &cluster)
	if err != nil {
		return nil, "", err
	}
	return &cluster, newETag, nil
}

// DeleteCluster deletes a cluster.
func (c *Client) DeleteCluster(ctx context.Context, name string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("clusters", name), nil, nil)
	return err
}
