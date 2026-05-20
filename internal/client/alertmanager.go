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
// the per-client alertmanager mutex and retries on 412 against the
// shared wrapper ETag — see [Client.putAlertmanagerWithRetry] for the
// full optimistic-locking rationale.
func (c *Client) PutAlertmanagerReceiver(ctx context.Context, name, etag string, recv AlertmanagerReceiver) (AlertmanagerReceiver, string, error) {
	c.alertmanagerMu.Lock()
	defer c.alertmanagerMu.Unlock()
	var out AlertmanagerReceiver
	newETag, err := c.putAlertmanagerWithRetry(ctx, c.tenantPath("alertmanager", "receivers", name), etag, recv, &out)
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

// PutAlertmanagerRoutes replaces the entire child route list. Holds the
// per-client alertmanager mutex and retries on 412 against the shared
// wrapper ETag — see [Client.putAlertmanagerWithRetry] for the full
// optimistic-locking rationale.
func (c *Client) PutAlertmanagerRoutes(ctx context.Context, etag string, routes []json.RawMessage) ([]json.RawMessage, string, error) {
	c.alertmanagerMu.Lock()
	defer c.alertmanagerMu.Unlock()
	var out rawRouteList
	newETag, err := c.putAlertmanagerWithRetry(ctx, c.tenantPath("alertmanager", "routes"), etag, rawRouteList{Items: routes}, &out)
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

// PutAlertmanagerGlobal replaces the global section. Holds the
// per-client alertmanager mutex and retries on 412 against the shared
// wrapper ETag — see [Client.putAlertmanagerWithRetry] for the full
// optimistic-locking rationale.
func (c *Client) PutAlertmanagerGlobal(ctx context.Context, etag string, g AlertmanagerGlobal) (AlertmanagerGlobal, string, error) {
	c.alertmanagerMu.Lock()
	defer c.alertmanagerMu.Unlock()
	var out AlertmanagerGlobal
	newETag, err := c.putAlertmanagerWithRetry(ctx, c.tenantPath("alertmanager", "global"), etag, g, &out)
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

// maxAlertmanagerPutRetries bounds the optimistic-locking retry loop in
// putAlertmanagerWithRetry. The terraform provider exposes three
// resources backed by one server-side wrapper ETag (receiver, routes,
// global), and a parallel apply or destroy can produce up to three
// concurrent racers. Each successful sibling write bumps the wrapper
// ETag, so a single retry isn't enough — observed in practice during
// `tofu destroy` where routes' retry landed too late after global's
// retry had already incremented the wrapper.
const maxAlertmanagerPutRetries = 4

// putAlertmanagerWithRetry executes a PUT against an alertmanager
// subresource (receiver/routes/global), retrying on 412 by refreshing
// the wrapper ETag between attempts.
//
// The wrapper ETag is the optimistic-locking unit shared across all
// three subresource paths. Within a single provider process, the
// per-client mutex serialises writes — but the caller's etag (read
// before acquiring the mutex) can still be stale by the time the write
// runs if a sibling resource has just incremented the wrapper. We
// retry up to [maxAlertmanagerPutRetries] times to handle that case.
//
// External mutators (console UI, another concurrent provider) still
// surface as a 412 once retries are exhausted, with the same
// actionable error message the caller would have seen on the first
// attempt.
func (c *Client) putAlertmanagerWithRetry(ctx context.Context, path, etag string, body, out any) (string, error) {
	currentETag := etag
	var lastErr error
	for attempt := 0; attempt < maxAlertmanagerPutRetries; attempt++ {
		newETag, err := c.requestWithETag(ctx, http.MethodPut, path, currentETag, body, out)
		if err == nil {
			return newETag, nil
		}
		lastErr = err
		// Only the 412 path is retryable. Non-412 errors (network, 5xx,
		// validation 400, etc.) get returned immediately. Same for the
		// initial empty-etag case — a "create" attempt that fails should
		// not loop.
		if currentETag == "" || !IsPreconditionFailed(err) {
			return "", err
		}
		refreshedETag, refreshErr := c.refreshAlertmanagerETag(ctx, http.MethodGet, path)
		if refreshErr != nil {
			return "", lastErr
		}
		currentETag = refreshedETag
	}
	return "", lastErr
}
