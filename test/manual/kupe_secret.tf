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
# Import by name and check a subsequent plan is empty with the same keys.

variable "smoke_secret_values" {
  description = "Complete non-production smoke values, with username and password keys."
  type        = map(string)
  sensitive   = true
  ephemeral   = true
}

resource "kupe_secret" "smoke" {
  name        = "smoke-secret"
  secret_path = "smoke/test-credentials"

  values_wo         = var.smoke_secret_values
  values_wo_version = 1

  sync = [
    {
      cluster   = kupe_cluster.smoke.name
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
