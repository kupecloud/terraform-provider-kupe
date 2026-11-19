resource "kupe_cluster" "prod" {
  name    = "prod"
  version = "1.36"

  resources = {
    cpu     = "4"
    memory  = "16Gi"
    storage = "100Gi"
  }
}

# An HA cluster keeps its Kubernetes API available during maintenance or a
# single-node failure. HA adds an hourly charge; see your plan's HA rate.
#
# high_availability can only be set when the cluster is created. Changing it
# later replaces the cluster (destroy, then create). To move to HA, create a
# new HA cluster and move your workloads to it.
resource "kupe_cluster" "prod_eu1" {
  name              = "prod-eu1"
  version           = "1.36"
  high_availability = true

  resources = {
    cpu     = "8"
    memory  = "32Gi"
    storage = "200Gi"
  }
}

# ha_phase summarises HA health, so automation can act on it without
# reading individual conditions.
output "prod_eu1_ha_phase" {
  description = "pending, ha-healthy, ha-degraded or ha-unavailable"
  value       = kupe_cluster.prod_eu1.ha_phase
}

output "prod_eu1_ha_ready" {
  description = "Ready control-plane replicas, as N of M"
  value       = "${kupe_cluster.prod_eu1.ha_replicas_ready} of ${kupe_cluster.prod_eu1.ha_replicas_desired}"
}
