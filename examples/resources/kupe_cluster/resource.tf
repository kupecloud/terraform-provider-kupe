resource "kupe_cluster" "production" {
  name    = "production"
  version = "1.32"

  resources = {
    cpu     = "4"
    memory  = "16Gi"
    storage = "100Gi"
  }
}

# HA cluster: 3 replicas, chart-managed external etcd StatefulSet, hard
# anti-affinity, encrypted etcd at rest. Adds an hourly charge — see
# your plan's HA rate.
#
# `high_availability` is create-time-only. Changing this attribute on an
# existing resource forces Terraform to replace (destroy + create) the
# cluster — there is no in-place migration. Plan a blue-green swap via
# GitOps if you need HA on an existing cluster.
resource "kupe_cluster" "prod_eu1" {
  name              = "prod-eu1"
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
  description = "One of pending | ha-healthy | ha-degraded | ha-unavailable"
  value       = kupe_cluster.prod_eu1.ha_phase
}

output "prod_eu1_ha_ready" {
  description = "N of M control-plane replicas currently ready"
  value       = "${kupe_cluster.prod_eu1.ha_replicas_ready} of ${kupe_cluster.prod_eu1.ha_replicas_desired}"
}
