# Smoke test for kupe_secret.
#
# Engineer-run release smoke against the authorised test environment only.
# Use OpenTofu 1.11 or Terraform 1.11.1 or later. Supply non-empty username
# and password keys through the ephemeral sensitive variable below.
# Start with an absent smoke/test-credentials entry; to adopt an existing
# entry, apply once without values_wo and values_wo_version, then add them.
# Bump values_wo_version to send new values. Changing the values without
# bumping that counter must produce no PUT. A concurrent Console edit after
# a saved plan must cause a conflict and preserve the previous counter.
# Keep the actual values out of this file and command-line history.
# Verify both keys reach default/smoke-secret after apply. Remove username
# from keys and apply: the same definition updates in place and sync_status
# reaches Synced before apply returns. Remove keys to deliver the whole entry.
# Remove the sync target and apply: wait for Withdrawing to disappear; the
# cluster copy goes shortly after. Destroy keeps the stored Vault values.
# Import by name and check a subsequent plan is empty with the same keys. The
# plan is empty only once values_wo and values_wo_version are removed from
# config (imported state has no counter), as in the adoption note above.
#
# Cluster: by default the fixture creates its own cluster, so the tenant
# needs room for one more. A starter tenant allows one cluster, and the plan
# refuses a second (400 plan_limit_reached). There, set existing_cluster to
# the name of a cluster the tenant already has, for example
#   export TF_VAR_existing_cluster=<cluster-name>
# and no cluster is created or destroyed.

variable "smoke_secret_values" {
  description = "Complete non-production smoke values, with username and password keys."
  type        = map(string)
  sensitive   = true
  ephemeral   = true
}

variable "existing_cluster" {
  description = "Name of an existing cluster to sync to. Empty creates a cluster for this fixture."
  type        = string
  default     = ""
}

# Created only when existing_cluster is unset, so a single-cluster tenant can
# run the fixture against the cluster it already has.
resource "kupe_cluster" "secret_smoke" {
  count = var.existing_cluster == "" ? 1 : 0

  name    = "smoke-secret-cluster"
  version = "1.35"

  resources = {
    cpu     = "2"
    memory  = "4Gi"
    storage = "20Gi"
  }
}

locals {
  secret_smoke_cluster = var.existing_cluster != "" ? var.existing_cluster : kupe_cluster.secret_smoke[0].name
}

resource "kupe_secret" "smoke" {
  name        = "smoke-secret"
  secret_path = "smoke/test-credentials"

  values_wo         = var.smoke_secret_values
  values_wo_version = 1

  sync = [
    {
      cluster   = local.secret_smoke_cluster
      namespace = "default"
      keys      = ["username", "password"]
    },
  ]
}

output "smoke_secret_source_version" {
  description = "Vault version of the stored entry, read from metadata only."
  value       = kupe_secret.smoke.source_version
}

output "smoke_secret_sync_status" {
  description = "Per-target delivery status after the current values have reached the namespace."
  value       = kupe_secret.smoke.sync_status
}
