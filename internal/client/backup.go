package client

import (
	"context"
	"net/http"
)

// Backup is a manual backup of a database. It is kept until it is deleted,
// even after its database is deleted, and goes with the tenant. The API
// never says where a backup is stored.
type Backup struct {
	Name string `json:"name"`
	// Database is the name of the database the backup was taken of. It may
	// have been deleted since.
	Database string `json:"database"`
	// Reason is Manual for a backup the tenant took, PreUpgrade for one the
	// platform took before a major upgrade.
	Reason          string        `json:"reason"`
	Status          *BackupStatus `json:"status,omitempty"`
	ResourceVersion string        `json:"resourceVersion"`
	CreatedAt       string        `json:"createdAt"`
}

// BackupStatus is a backup's status.
type BackupStatus struct {
	// Phase is Pending, Running, Completed, Failed or Deleting. A backup can
	// be restored from once it is Completed.
	Phase string `json:"phase"`
	// CompletedAt is when the backup finished (RFC 3339), "" until then.
	CompletedAt string `json:"completedAt"`
	// SizeBytes is the backup's size, 0 until measured.
	SizeBytes int64 `json:"sizeBytes"`
	// Source is the database as it was when the backup started, nil until
	// then.
	Source *BackupSource `json:"source,omitempty"`
	// DatabaseDeleted is true once the database the backup was taken of is
	// deleted or being deleted.
	DatabaseDeleted bool `json:"databaseDeleted"`
	// Conditions include Ready, whose message says what the backup waits for
	// or why it failed.
	Conditions []Condition `json:"conditions"`
}

// BackupSource is the database a backup was taken of, as it was then.
type BackupSource struct {
	Name string `json:"name"`
	// Version is the PostgreSQL major version; a database restored from the
	// backup runs it.
	Version string `json:"version"`
	// Storage is the database's storage; a database restored from the
	// backup needs at least this much.
	Storage string `json:"storage"`
}

// DatabaseSource makes a new database a restore: of a manual backup, or of
// another database at a point in its restore window (the latest when
// PointInTime is ""). Exactly one of Backup and Database is set. The API
// echoes it on a restored database, with PointInTime in UTC.
type DatabaseSource struct {
	Backup      string `json:"backup,omitempty"`
	Database    string `json:"database,omitempty"`
	PointInTime string `json:"pointInTime,omitempty"`
}

// createBackupRequest is the body of a manual backup. Without a name the
// API names the backup after the database and the time.
type createBackupRequest struct {
	Name string `json:"name,omitempty"`
}

// CreateDatabaseBackup takes a manual backup of a database. An empty name
// lets the API choose one.
func (c *Client) CreateDatabaseBackup(ctx context.Context, database, name string) (*Backup, string, error) {
	var backup Backup
	etag, err := c.request(ctx, http.MethodPost, c.tenantPath("databases", database, "backups"),
		createBackupRequest{Name: name}, &backup)
	if err != nil {
		return nil, "", err
	}
	return &backup, etag, nil
}

// GetBackup returns a manual backup by name.
func (c *Client) GetBackup(ctx context.Context, name string) (*Backup, string, error) {
	var backup Backup
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("backups", name), nil, &backup)
	if err != nil {
		return nil, "", err
	}
	return &backup, etag, nil
}

// DeleteBackup deletes a manual backup. A backup already being deleted is
// accepted again.
func (c *Client) DeleteBackup(ctx context.Context, name string) error {
	_, err := c.request(ctx, http.MethodDelete, c.tenantPath("backups", name), nil, nil)
	return err
}
