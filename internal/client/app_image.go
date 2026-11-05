package client

import (
	"context"
	"fmt"
	"net/http"
)

// ImagePlatform identifies one manifest in the resolved image index.
type ImagePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
	Digest       string `json:"digest"`
}

// ImageResolution preserves the immutable index or single-manifest identity.
// Platforms is a bounded summary, not an exhaustive list of supported platforms.
type ImageResolution struct {
	Reference    string          `json:"reference"`
	Platforms    []ImagePlatform `json:"platforms"`
	User         string          `json:"user"`
	ExposedPorts []int           `json:"exposedPorts"`
}

// ResolveAppImage reads image metadata through the tenant's Kupe API. It never
// contacts a registry directly or supplies registry credentials.
func (c *Client) ResolveAppImage(ctx context.Context, image string) (*ImageResolution, error) {
	var result ImageResolution
	_, err := c.request(ctx, http.MethodPost, c.tenantPath("app-images", "resolve"), struct {
		Image string `json:"image"`
	}{Image: image}, &result)
	if err != nil {
		return nil, fmt.Errorf("resolve app image: %w", err)
	}
	return &result, nil
}
