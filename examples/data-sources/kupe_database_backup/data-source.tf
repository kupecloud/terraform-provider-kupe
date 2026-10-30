data "kupe_database_backup" "before_migration" {
  name = "orders-db-before-migration"
}

# A database restored from this backup needs at least this much storage.
output "restore_needs_storage" {
  value = data.kupe_database_backup.before_migration.source_storage
}
