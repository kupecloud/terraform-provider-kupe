# Smoke test for kupe_database, kupe_database_binding and data.kupe_database.
#
# Verifies:
#   - 201 from POST /databases; phase reaches Running
#   - update path: changing resources or high_availability is a PATCH, not a replace
#   - binding: phase Ready, Service and Secret appear in the bound namespace
#   - destroy: the binding and the database are removed
#
# deletion_protection is off so `tofu destroy` cleans up.

resource "kupe_database" "smoke" {
  name                = "smoke-db"
  deletion_protection = false

  resources = {
    cpu     = "250m"
    memory  = "512Mi"
    storage = "5Gi"
  }
}

resource "kupe_database_binding" "smoke" {
  database  = kupe_database.smoke.name
  cluster   = kupe_cluster.smoke.name
  namespace = "default"
  role      = "app"
}

data "kupe_database" "smoke_read" {
  name = kupe_database.smoke.name
}

output "smoke_database_phase" {
  description = "Phase reported by the data source; Running once provisioned."
  value       = data.kupe_database.smoke_read.phase
}

output "smoke_database_binding_host" {
  description = "In-cluster host the bound namespace connects to."
  value       = kupe_database_binding.smoke.host
}
