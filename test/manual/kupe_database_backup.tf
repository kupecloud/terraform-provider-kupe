# Smoke test for kupe_database_backup, data.kupe_database_backup and a
# restore from a backup. Needs kupe_database.smoke (kupe_database.tf).
#
# Verifies:
#   - 201 from POST /databases/smoke-db/backups; phase reaches Completed
#   - source_version and source_storage are set once the backup completes
#   - restore: smoke-db-restore, created with source.backup and no version,
#     reaches Running with the backup's source_version
#   - destroy: the restored database and the backup are deleted
#
# deletion_protection is off so `tofu destroy` cleans up.

resource "kupe_database_backup" "smoke" {
  database = kupe_database.smoke.name
}

data "kupe_database_backup" "smoke_read" {
  name = kupe_database_backup.smoke.name
}

resource "kupe_database" "smoke_restore" {
  name                = "smoke-db-restore"
  deletion_protection = false

  source = {
    backup = kupe_database_backup.smoke.name
  }

  resources = {
    cpu     = "250m"
    memory  = "512Mi"
    storage = "5Gi"
  }
}

output "smoke_backup_phase" {
  description = "Phase reported by the data source; Completed once the backup is done."
  value       = data.kupe_database_backup.smoke_read.phase
}

output "smoke_restore_version" {
  description = "Version of the restored database; the backup's source_version."
  value       = kupe_database.smoke_restore.version
}
