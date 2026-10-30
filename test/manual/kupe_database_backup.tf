# Smoke test for kupe_database_backup, data.kupe_database_backup and
# restores. Needs kupe_database.smoke (kupe_database.tf).
#
# Verifies:
#   - 201 from POST /databases/smoke-db/backups; phase reaches Completed
#   - source_version and source_storage are set once the backup completes
#   - restore from the backup: smoke-db-restore, created with source.backup
#     and no version, reaches Running with the backup's source_version
#   - point-in-time restore: smoke-db-pitr, created with source.database and
#     a literal point_in_time, reaches Running; a second `tofu plan` is empty
#     (the API echoes source.point_in_time unchanged)
#   - destroy: the restored databases and the backup are deleted
#
# Before applying, set point_in_time in smoke_pitr to a time (UTC, whole
# seconds) inside smoke-db's recovery_window.
#
# The backup has a fixed name, so the restores' source is known at plan
# time and never replaces them. The restored databases keep deletion
# protection on, so a planned replacement fails instead of deleting them.
# To destroy them:
#   tofu apply -var restore_deletion_protection=false
#   tofu destroy

variable "restore_deletion_protection" {
  description = "Deletion protection of the restored smoke databases; set false before destroying them."
  type        = bool
  default     = true
}

resource "kupe_database_backup" "smoke" {
  database = kupe_database.smoke.name
  name     = "smoke-db-manual"
}

data "kupe_database_backup" "smoke_read" {
  name = kupe_database_backup.smoke.name
}

resource "kupe_database" "smoke_restore" {
  name                = "smoke-db-restore"
  deletion_protection = var.restore_deletion_protection

  source = {
    backup = kupe_database_backup.smoke.name
  }

  resources = {
    cpu     = "250m"
    memory  = "512Mi"
    storage = "5Gi"
  }
}

resource "kupe_database" "smoke_pitr" {
  name                = "smoke-db-pitr"
  deletion_protection = var.restore_deletion_protection

  source = {
    database      = kupe_database.smoke.name
    point_in_time = "2026-10-30T09:00:00Z"
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

output "smoke_pitr_point" {
  description = "The point smoke-db-pitr was restored to, as the API echoes it."
  value       = kupe_database.smoke_pitr.source.point_in_time
}
