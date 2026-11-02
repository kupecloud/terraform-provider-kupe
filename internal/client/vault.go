package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

const (
	CodeSecretVersionConflict  = "secret_version_conflict"  // #nosec G101 -- error code, not a credential
	CodeSecretWriteUnconfirmed = "secret_write_unconfirmed" // #nosec G101 -- error code, not a credential
	CodeSecretPathReserved     = "secret_path_reserved"     // #nosec G101 -- error code, not a credential
	CodeVaultUnavailable       = "vault_unavailable"
	CodeVaultNotConfigured     = "vault_not_configured"
)

// VaultEntry contains metadata only. The provider never requests values.
type VaultEntry struct {
	Path       string   `json:"path"`
	Version    int64    `json:"version"`
	Deleted    bool     `json:"deleted"`
	References []string `json:"references"`
}

// VaultWrite confirms a complete replacement and the definitions stamped
// for delivery. It deliberately has no field capable of retaining values.
type VaultWrite struct {
	Path             string   `json:"path"`
	Version          int64    `json:"version"`
	RefreshRequested []string `json:"refreshRequested"`
}

func (c *Client) vaultPath(path string) string {
	return c.tenantPath(append([]string{"vault"}, strings.Split(path, "/")...)...)
}

// GetVaultEntry reads metadata; no values query is ever sent.
func (c *Client) GetVaultEntry(ctx context.Context, path string) (*VaultEntry, error) {
	var entry VaultEntry
	_, err := c.request(ctx, http.MethodGet, c.vaultPath(path), nil, &entry)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// PutVaultValues performs exactly one conditional replacement. A retry of a
// committed write would manufacture a conflict and lose the confirmed version.
func (c *Client) PutVaultValues(ctx context.Context, path string, values map[string]string, ifMatch *int64) (*VaultWrite, error) {
	ctx = context.WithValue(ctx, noRetryContextKey{}, true)
	match, noneMatch := "", "*"
	if ifMatch != nil {
		match, noneMatch = fmt.Sprintf("\"%d\"", *ifMatch), ""
	}
	var result VaultWrite
	_, err := c.requestWithPrecondition(ctx, http.MethodPut, c.vaultPath(path), match, noneMatch,
		struct {
			Values map[string]string `json:"values"`
		}{Values: values}, &result)
	if err != nil {
		return nil, err
	}
	// A successful status without a usable version cannot confirm the write.
	if result.Version < 1 {
		return nil, fmt.Errorf("vault write returned no confirmed version")
	}
	return &result, nil
}
