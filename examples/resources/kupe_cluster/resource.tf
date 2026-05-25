resource "kupe_cluster" "production" {
  name         = "production"
  display_name = "Production"
  type         = "shared"
  version      = "1.32"

  resources = {
    cpu     = "4"
    memory  = "16Gi"
    storage = "100Gi"
  }
}

# HA cluster: 3 replicas, embedded etcd, hard anti-affinity. Adds an hourly
# charge — see your plan's HA rate.
#
# Enabling on an existing cluster triggers an in-place kine→etcd migration
# with ~10 minutes of API downtime. Disabling is not supported in v1.
resource "kupe_cluster" "prod_eu1" {
  name              = "prod-eu1"
  display_name      = "Production EU1"
  type              = "shared"
  version           = "1.35"
  high_availability = true

  resources = {
    cpu     = "8"
    memory  = "32Gi"
    storage = "200Gi"
  }
}

# Expose the consumer-friendly rollup so downstream automation can branch
# on operational state without inspecting individual conditions.
output "prod_eu1_ha_phase" {
  description = "One of pending | migrating | ha-healthy | ha-degraded | ha-unavailable"
  value       = kupe_cluster.prod_eu1.ha_phase
}

output "prod_eu1_ha_ready" {
  description = "N of M control-plane replicas currently ready"
  value       = "${kupe_cluster.prod_eu1.ha_replicas_ready} of ${kupe_cluster.prod_eu1.ha_replicas_desired}"
}
