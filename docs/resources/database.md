---
page_title: "kupe_database Resource - kupe"
subcategory: ""
description: |-
  Manages a Kupe PostgreSQL database. Create and Update wait for the database to reach phase=Running with the change in effect on every instance. They fail at once if the database is being deleted (Terminating), and after 5 minutes if it stays Degraded once the platform has picked up the change. If the timeout fires first, the apply fails; the platform carries on in the background, and a database that timed out on Create stays in state, marked tainted. A change to deletion_protection alone takes effect at once and is not waited for. Delete waits until the database is gone. Set timeouts with the timeouts attribute, for example timeouts = { create = "45m" } (defaults: create/update 30m, delete 15m).
  Applications reach the database through kupe_database_binding, which places a Service and a credentials Secret in each namespace it lists, in one cluster. Credentials are never returned by the API or stored in Terraform state.
  Destroy semantics
  Deleting a database removes it and its automatic backups immediately, and they cannot be restored. Manual backups stay until you delete them. deletion_protection (default true) makes the Kupe platform refuse the delete: set it to false and apply before destroying. Add lifecycle { prevent_destroy = true } to stop Terraform from planning the destroy at all.
---

# kupe_database (Resource)

Manages a Kupe PostgreSQL database. Create and Update wait for the database to reach phase=Running with the change in effect on every instance. They fail at once if the database is being deleted (Terminating), and after 5 minutes if it stays Degraded once the platform has picked up the change. If the timeout fires first, the apply fails; the platform carries on in the background, and a database that timed out on Create stays in state, marked tainted. A change to `deletion_protection` alone takes effect at once and is not waited for. Delete waits until the database is gone. Set timeouts with the `timeouts` attribute, for example `timeouts = { create = "45m" }` (defaults: create/update 30m, delete 15m).

Applications reach the database through `kupe_database_binding`, which places a Service and a credentials Secret in each namespace it lists, in one cluster. Credentials are never returned by the API or stored in Terraform state.

### Destroy semantics

Deleting a database removes it and its automatic backups immediately, and they cannot be restored. Manual backups stay until you delete them. `deletion_protection` (default `true`) makes the Kupe platform refuse the delete: set it to `false` and apply before destroying. Add `lifecycle { prevent_destroy = true }` to stop Terraform from planning the destroy at all.

## Example Usage

```terraform
resource "kupe_database" "orders" {
  name    = "orders-db"
  version = "18"

  resources = {
    cpu     = "500m"
    memory  = "1Gi"
    storage = "10Gi"
  }

  # Two instances with a synchronous standby. Can be turned on or off later.
  high_availability = true

  # Keep 14 days of backups instead of the default 7.
  backup = {
    retention = 14
  }

  # deletion_protection defaults to true: the platform refuses to delete the
  # database until it is set to false and applied.
}
```

### Protecting a database from destroy

`deletion_protection` makes the Kupe platform refuse the delete. `prevent_destroy` stops Terraform from planning it in the first place, so use both for databases you cannot lose.

```terraform
resource "kupe_database" "billing" {
  name = "billing-db"

  resources = {
    cpu     = "1"
    memory  = "2Gi"
    storage = "50Gi"
  }

  # Stop Terraform from planning a destroy at all, on top of the platform's
  # deletion protection.
  lifecycle {
    prevent_destroy = true
  }
}
```

To delete a protected database, set `deletion_protection = false` (and remove `prevent_destroy`), run `terraform apply`, then destroy it.

### Backups

Kupe takes a full backup of every database each day and saves the write-ahead log (WAL) continuously in between, so the backups cover every point inside the retention period. `backup.retention` sets how far back they reach (7 to 35 days, default 7) and `backup.schedule` how many full backups are kept within that time (`Daily` or `DailyThenWeekly`). Shortening retention deletes the backups that fall outside the new window.

A change applies in place, and `terraform apply` waits for the database to take it, as it does for a resize. Fields you leave out keep their current value, and removing `backup` keeps the current settings: to go back to the defaults, set them.

`last_backup_at`, `recovery_window` and `backup_size_bytes` report the newest full backup, the restore window (the oldest and newest points you can restore to), and the storage the backups take up. The `kupe_database` data source returns the same values.

### Restoring from a backup or another database

`source` creates the database from a manual backup (`source.backup`) or from another database at a point inside its restore window (`source.database` and `source.point_in_time`; without a point, the latest one), instead of starting it empty. The source is unchanged.

```terraform
# A new database restored from a manual backup. It runs the backup's major
# version, so `version` is left unset. The backup resource sets `name`, so
# this reference is known when Terraform plans; a changing `source` would
# replace the database. Once the database exists, `source` can be removed.
resource "kupe_database" "orders_copy" {
  name = "orders-copy"

  source = {
    backup = kupe_database_backup.before_migration.name
  }

  resources = {
    cpu     = "500m"
    memory  = "1Gi"
    storage = "10Gi" # at least the backup's source_storage
  }
}

# A new database holding the data orders-db had at 09:00 UTC. The time is
# written literally: a computed one, such as timestamp(), would replace the
# database on every apply.
resource "kupe_database" "orders_at_nine" {
  name = "orders-at-nine"

  source = {
    database      = kupe_database.orders.name
    point_in_time = "2026-10-30T09:00:00Z"
  }

  resources = {
    cpu     = "500m"
    memory  = "1Gi"
    storage = "10Gi" # at least the storage of orders-db
  }
}
```

- The new database runs the source's major version. Leave `version` unset, or set that version.
- Its storage must be at least the source's: a backup's `source_storage`, or the `resources.storage` of the source database.
- `point_in_time` is in UTC with whole seconds, for example `2026-10-30T09:00:00Z`, between the source database's `recovery_window.earliest` and `recovery_window.latest`.
- A restore copies the source's data, so it takes longer than creating an empty database. Raise `timeouts.create` for a large one.

#### Keep `source` stable

`source` is read only when the database is created. Any change to it replaces the database: Terraform deletes it, with the data written since the restore and its automatic backups, and restores a new one. A value Terraform knows only at apply time counts as a change, so these replace a restored database:

- `source.backup` refers to a `kupe_database_backup` without `name`, and that backup is taken again (for example after it was deleted in the Kupe Console). Its new name is not known until apply.
- `source.point_in_time` is computed when Terraform plans, for example with `timestamp()`. It changes on every plan.

Once the restored database exists, remove `source` from the configuration: the database keeps what it was restored from and the plan is empty. To keep `source` in the configuration, name the backup with a literal or with a backup resource that sets `name`, and write `point_in_time` literally.

Leave `deletion_protection` at `true`, its default. A planned replacement of a protected database then fails at apply instead of deleting it.

Take manual backups with [`kupe_database_backup`](database_backup.md).

### Connecting applications

Grant access with [`kupe_database_binding`](database_binding.md). Each binding places a Service named after the database and a credentials Secret into each namespace it lists, in one cluster.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `name` (String) Database name (immutable after creation). A lowercase DNS label starting with a letter, without `--`, at most 44 characters. It is also the Service name applications connect to. The Kupe platform also limits it to 56 characters minus the length of the tenant name, so tenant names over 12 characters allow fewer than 44; the provider cannot check that at plan time, so the platform refuses a longer name when the database is created.
- `resources` (Attributes) Per-instance resources in Kubernetes quantity format. Billing uses these requested values. A high-availability database runs two instances of this size. All three are required, unlike the optional limits of `kupe_cluster`. (see [below for nested schema](#nestedatt--resources))

### Optional

- `backup` (Attributes) Backup settings: how far back the backups reach and how many full backups are kept. Fields left out keep their current value, and removing `backup` from the configuration keeps the current settings. Null on a database that has never set them: the defaults apply, 7 days and `Daily`. While the Kupe platform does not offer backups, setting `backup` fails with error code `backups_unavailable`. (see [below for nested schema](#nestedatt--backup))
- `deletion_protection` (Boolean) While `true`, the Kupe platform refuses to delete the database. Set it to `false` and apply before destroying. Default `true`, also for an existing database: importing an unprotected database without this attribute in configuration plans turning protection on.
- `engine` (String) Database engine. Only `postgres` is supported, and it is the default. The engine of an existing database cannot change: a different value fails the plan instead of replacing the database.
- `high_availability` (Boolean) Run two instances with a synchronous standby. Can be turned on or off in place. When unset, a new database is created without it and an existing or imported database keeps its current setting, so removing the attribute from configuration does not turn HA off: set it to `false` to do that.
- `source` (Attributes) Restore the new database from a manual backup or from another database, instead of starting it empty. Set exactly one of `backup` and `database`. The new database runs the source's major version: leave `version` unset, or set that one. Its storage must be at least the source's. The source is unchanged, and the new database counts against the plan's database limit like any other. `source` is read only when the database is created, and any change to it replaces the database: Terraform deletes it and restores a new one. That includes a value Terraform knows only at apply time, such as the name of a `kupe_database_backup` without `name` that is taken again. Once the database exists, remove `source` from the configuration (the database keeps it), or keep it constant: name the backup with a literal or a backup resource that sets `name`, and write `point_in_time` literally. Null on a database that was not restored. While the Kupe platform does not offer backups, setting `source` fails with error code `backups_unavailable`. (see [below for nested schema](#nestedatt--source))
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))
- `version` (String) Engine major version, e.g. `18`. When unset, the platform default is used and recorded here; a database restored with `source` runs the source's major version. Major version upgrades are not available yet, so changing the version of an existing database fails the plan.

### Read-Only

- `backup_size_bytes` (Number) Bytes every full backup and all WAL kept for the database take up in storage, manual backups included. 0 until measured.
- `created_at` (String) Timestamp when the database was created.
- `current_version` (String) Full engine version running, e.g. `18.0`.
- `etag` (String) Resource version used for optimistic locking during updates.
- `last_backup_at` (String) When the newest full backup finished (RFC 3339). Empty until the first backup completes.
- `phase` (String) Current phase: Pending, Provisioning, Running, Degraded or Terminating.
- `recovery_window` (Attributes) The restore window: the oldest and newest points you can restore the database to. Null until the backups report them. (see [below for nested schema](#nestedatt--recovery_window))

<a id="nestedatt--resources"></a>
### Nested Schema for `resources`

Required:

- `cpu` (String) CPU without a unit, e.g. `500m` or `2`.
- `memory` (String) Memory with a unit, e.g. `1Gi` or `512Mi`.
- `storage` (String) Storage with a unit, e.g. `10Gi`. Storage can only grow: a smaller value fails the plan.


<a id="nestedatt--backup"></a>
### Nested Schema for `backup`

Optional:

- `retention` (Number) How far back the backups reach, in days: 7 to 35, default 7. The Kupe platform sets the range and refuses a value outside it (error code `DATABASE_BACKUP_RETENTION_OUT_OF_RANGE`). Shortening retention deletes the backups that fall outside the new window.
- `schedule` (String) How many full backups are kept within the retention period: `Daily` keeps one for every day; `DailyThenWeekly` keeps one for each of the last 7 days and one a week before that. The points you can restore to are the same either way; restoring to an older point takes longer. Default `Daily`.


<a id="nestedatt--source"></a>
### Nested Schema for `source`

Optional:

- `backup` (String) Name of a manual backup to restore. The database it was taken of may have been deleted. The new database holds the data as it was when the backup completed.
- `database` (String) Name of a database to restore. The new database holds its data as it was at `point_in_time`, or at the latest point when that is unset.
- `point_in_time` (String) The moment to restore `database` to, in UTC with whole seconds, for example `2026-10-30T09:00:00Z`. It must be inside that database's restore window (its `recovery_window`). Leave it unset to restore to the latest point. Only with `database`. Write it literally: a time computed when Terraform plans, such as `timestamp()`, changes on every plan and replaces the database each time.


<a id="nestedatt--timeouts"></a>
### Nested Schema for `timeouts`

Optional:

- `create` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).
- `delete` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours). Setting a timeout for a Delete operation is only applicable if changes are saved into state before the destroy operation occurs.
- `update` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).


<a id="nestedatt--recovery_window"></a>
### Nested Schema for `recovery_window`

Read-Only:

- `earliest` (String) The oldest point you can restore to (RFC 3339).
- `latest` (String) The newest point you can restore to: the newest WAL saved (RFC 3339).

## Import

Import is supported using the following syntax:

```shell
terraform import kupe_database.orders orders-db
```
