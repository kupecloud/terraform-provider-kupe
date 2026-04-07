package client

import (
	"context"
	"net/http"
)

// Cluster represents a managed cluster in the API response.
type Cluster struct {
	Name            string           `json:"name"`
	DisplayName     string           `json:"displayName"`
	Type            string           `json:"type"`
	Version         string           `json:"version"`
	Resources       *ClusterResource `json:"resources,omitempty"`
	Alerts          any              `json:"alerts,omitempty"`
	Status          *ClusterStatus   `json:"status,omitempty"`
	ResourceVersion string           `json:"resourceVersion"`
	CreatedAt       string           `json:"createdAt"`
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
}

// CreateClusterRequest is the body for creating a cluster.
type CreateClusterRequest struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"displayName"`
	Type        string           `json:"type"`
	Version     string           `json:"version,omitempty"`
	Resources   *ClusterResource `json:"resources,omitempty"`
	Alerts      any              `json:"alerts,omitempty"`
}

// PatchClusterRequest is the body for updating a cluster.
type PatchClusterRequest struct {
	Version   *string          `json:"version,omitempty"`
	Resources *ClusterResource `json:"resources,omitempty"`
	Alerts    any              `json:"alerts,omitempty"`
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
func (c *Client) UpdateCluster(ctx context.Context, name string, etag string, req PatchClusterRequest) (*Cluster, string, error) {
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
