# Smoke test for kupe_cluster.
#
# Verifies:
#   - 201 from POST /clusters
#   - operator reconciles status.phase to Ready (~5-8 min on dev)
#   - update path: changing version triggers a PATCH (not a recreate)
#   - destroy: kubectl get managedcluster -n tenant-kupe-test returns empty

resource "kupe_cluster" "smoke" {
  name         = "smoke-cluster"
  display_name = "Smoke Test Cluster"
  type         = "shared"
  version      = "1.32"

  resources = {
    cpu     = "2"
    memory  = "4Gi"
    storage = "20Gi"
  }
}

output "smoke_cluster_endpoint" {
  description = "API endpoint reported by the operator once the cluster is Ready."
  value       = kupe_cluster.smoke.endpoint
}

# Data source smoke — reads back the cluster we just created. The implicit
# dependency on kupe_cluster.smoke.name forces tofu to evaluate this after
# the resource exists, not at plan time.
data "kupe_cluster" "smoke_read" {
  name = kupe_cluster.smoke.name
}

output "smoke_cluster_phase" {
  description = "status.phase reported by the data source — should be Ready after operator reconcile."
  value       = data.kupe_cluster.smoke_read.phase
}
