package client

import (
	"context"
	"encoding/json"
	"net/http"
)

// AlertmanagerReceiver is the wire shape for a single named receiver. The
// API stores receivers as opaque maps so we mirror that here — the user
// authors the receiver as YAML and the provider passes it through. This
// keeps the provider compatible with new Alertmanager receiver types
// (Discord, Pushover, etc.) without a provider release.
type AlertmanagerReceiver map[string]any

// AlertmanagerRoute is the structured route shape returned by the API.
// Mirrors the alertmanager package's Route type but with JSON tags so the
// API/provider boundary uses snake_case to match the Alertmanager YAML
// schema users already know.
type AlertmanagerRoute struct {
	Receiver            string               `json:"receiver,omitempty"`
	GroupBy             []string             `json:"group_by,omitempty"`
	Continue            bool                 `json:"continue,omitempty"`
	Matchers            []string             `json:"matchers,omitempty"`
	Match               map[string]string    `json:"match,omitempty"`
	MatchRE             map[string]string    `json:"match_re,omitempty"`
	GroupWait           string               `json:"group_wait,omitempty"`
	GroupInterval       string               `json:"group_interval,omitempty"`
	RepeatInterval      string               `json:"repeat_interval,omitempty"`
	MuteTimeIntervals   []string             `json:"mute_time_intervals,omitempty"`
	ActiveTimeIntervals []string             `json:"active_time_intervals,omitempty"`
	Routes              []*AlertmanagerRoute `json:"routes,omitempty"`
}

// AlertmanagerGlobal is the global section as a generic map. Users author
// it as YAML and the provider passes the parsed map through.
type AlertmanagerGlobal map[string]any

// --- Receivers ---

// GetAlertmanagerReceiver fetches a single receiver by name. Returns the
// raw map and the wrapper ETag.
func (c *Client) GetAlertmanagerReceiver(ctx context.Context, name string) (AlertmanagerReceiver, string, error) {
	var recv AlertmanagerReceiver
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("alertmanager", "receivers", name), nil, &recv)
	if err != nil {
		return nil, "", err
	}
	return recv, etag, nil
}

// PutAlertmanagerReceiver creates or replaces a receiver by name.
func (c *Client) PutAlertmanagerReceiver(ctx context.Context, name, etag string, recv AlertmanagerReceiver) (AlertmanagerReceiver, string, error) {
	var out AlertmanagerReceiver
	newETag, err := c.requestWithETag(ctx, http.MethodPut, c.tenantPath("alertmanager", "receivers", name), etag, recv, &out)
	if err != nil {
		return nil, "", err
	}
	return out, newETag, nil
}

// DeleteAlertmanagerReceiver removes a receiver by name. Returns 409 if
// any route still references it; the caller should reorder its plan to
// delete the dependent route first.
func (c *Client) DeleteAlertmanagerReceiver(ctx context.Context, name string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("alertmanager", "receivers", name), nil, nil)
	return err
}

// --- Routes (whole-list resource) ---

// rawRouteList is the wire envelope used for routes. Items are kept as
// json.RawMessage so unknown fields survive the round-trip through the
// provider without being dropped — the provider is forward-compatible
// with new Alertmanager route fields without a release.
type rawRouteList struct {
	Items []json.RawMessage `json:"items"`
}

// GetAlertmanagerRoutes returns the full child route list of the root route.
func (c *Client) GetAlertmanagerRoutes(ctx context.Context) ([]json.RawMessage, string, error) {
	var list rawRouteList
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("alertmanager", "routes"), nil, &list)
	if err != nil {
		return nil, "", err
	}
	return list.Items, etag, nil
}

// PutAlertmanagerRoutes replaces the entire child route list.
func (c *Client) PutAlertmanagerRoutes(ctx context.Context, etag string, routes []json.RawMessage) ([]json.RawMessage, string, error) {
	var out rawRouteList
	newETag, err := c.requestWithETag(ctx, http.MethodPut, c.tenantPath("alertmanager", "routes"), etag, rawRouteList{Items: routes}, &out)
	if err != nil {
		return nil, "", err
	}
	return out.Items, newETag, nil
}

// --- Global ---

// GetAlertmanagerGlobal fetches the global section.
func (c *Client) GetAlertmanagerGlobal(ctx context.Context) (AlertmanagerGlobal, string, error) {
	var g AlertmanagerGlobal
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("alertmanager", "global"), nil, &g)
	if err != nil {
		return nil, "", err
	}
	return g, etag, nil
}

// PutAlertmanagerGlobal replaces the global section.
func (c *Client) PutAlertmanagerGlobal(ctx context.Context, etag string, g AlertmanagerGlobal) (AlertmanagerGlobal, string, error) {
	var out AlertmanagerGlobal
	newETag, err := c.requestWithETag(ctx, http.MethodPut, c.tenantPath("alertmanager", "global"), etag, g, &out)
	if err != nil {
		return nil, "", err
	}
	return out, newETag, nil
}
