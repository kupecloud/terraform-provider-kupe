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

// PutAlertmanagerReceiver creates or replaces a receiver by name. Holds
// the per-client alertmanager mutex and refreshes the wrapper ETag once
// on 412 — see [Client.alertmanagerMu] for the full rationale.
func (c *Client) PutAlertmanagerReceiver(ctx context.Context, name, etag string, recv AlertmanagerReceiver) (AlertmanagerReceiver, string, error) {
	c.alertmanagerMu.Lock()
	defer c.alertmanagerMu.Unlock()
	path := c.tenantPath("alertmanager", "receivers", name)
	var out AlertmanagerReceiver
	newETag, err := c.requestWithETag(ctx, http.MethodPut, path, etag, recv, &out)
	if err != nil && etag != "" && IsPreconditionFailed(err) {
		// Wrapper changed since the caller's last read. Refresh and retry
		// once under the same lock so we converge on the current state.
		// If the receiver itself doesn't exist yet (404 on the refresh),
		// fall back to a no-If-Match write to let the API create it.
		freshETag, refreshErr := c.refreshAlertmanagerETag(ctx, http.MethodGet, path)
		if refreshErr != nil {
			return nil, "", err
		}
		out = nil
		newETag, err = c.requestWithETag(ctx, http.MethodPut, path, freshETag, recv, &out)
	}
	if err != nil {
		return nil, "", err
	}
	return out, newETag, nil
}

// DeleteAlertmanagerReceiver removes a receiver by name. Returns 409 if
// any route still references it; the caller should reorder its plan to
// delete the dependent route first. The mutex prevents the read-side of
// a concurrent alertmanager mutation from racing the receiver delete.
func (c *Client) DeleteAlertmanagerReceiver(ctx context.Context, name string) error {
	c.alertmanagerMu.Lock()
	defer c.alertmanagerMu.Unlock()
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

// PutAlertmanagerRoutes replaces the entire child route list. See the
// receiver Put for the mutex + refresh-on-412 rationale.
func (c *Client) PutAlertmanagerRoutes(ctx context.Context, etag string, routes []json.RawMessage) ([]json.RawMessage, string, error) {
	c.alertmanagerMu.Lock()
	defer c.alertmanagerMu.Unlock()
	path := c.tenantPath("alertmanager", "routes")
	var out rawRouteList
	newETag, err := c.requestWithETag(ctx, http.MethodPut, path, etag, rawRouteList{Items: routes}, &out)
	if err != nil && etag != "" && IsPreconditionFailed(err) {
		freshETag, refreshErr := c.refreshAlertmanagerETag(ctx, http.MethodGet, path)
		if refreshErr != nil {
			return nil, "", err
		}
		out = rawRouteList{}
		newETag, err = c.requestWithETag(ctx, http.MethodPut, path, freshETag, rawRouteList{Items: routes}, &out)
	}
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

// PutAlertmanagerGlobal replaces the global section. See the receiver
// Put for the mutex + refresh-on-412 rationale.
func (c *Client) PutAlertmanagerGlobal(ctx context.Context, etag string, g AlertmanagerGlobal) (AlertmanagerGlobal, string, error) {
	c.alertmanagerMu.Lock()
	defer c.alertmanagerMu.Unlock()
	path := c.tenantPath("alertmanager", "global")
	var out AlertmanagerGlobal
	newETag, err := c.requestWithETag(ctx, http.MethodPut, path, etag, g, &out)
	if err != nil && etag != "" && IsPreconditionFailed(err) {
		freshETag, refreshErr := c.refreshAlertmanagerETag(ctx, http.MethodGet, path)
		if refreshErr != nil {
			return nil, "", err
		}
		out = nil
		newETag, err = c.requestWithETag(ctx, http.MethodPut, path, freshETag, g, &out)
	}
	if err != nil {
		return nil, "", err
	}
	return out, newETag, nil
}

// refreshAlertmanagerETag fetches the current wrapper ETag from any
// alertmanager subresource. The wrapper ETag is shared across receiver,
// routes, and global, so any GET works for the refresh. Used by the Put
// methods after a 412 to converge on the live ETag without unlocking.
func (c *Client) refreshAlertmanagerETag(ctx context.Context, method, path string) (string, error) {
	var discard json.RawMessage
	etag, err := c.request(ctx, method, path, nil, &discard)
	if err != nil && !IsNotFound(err) {
		return "", err
	}
	// A 404 on the subresource is fine — the wrapper may exist without
	// this specific section. The returned etag will be empty in that
	// case, which causes the retry to PUT without If-Match (a create).
	return etag, nil
}
