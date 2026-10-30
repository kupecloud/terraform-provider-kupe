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
