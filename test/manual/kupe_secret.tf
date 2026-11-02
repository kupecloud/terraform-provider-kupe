# Smoke test for kupe_secret.
#
# Engineer-run release smoke against the authorised test environment only.
# Before applying, put non-empty username and password keys at
# smoke/test-credentials using the Console. Keep values out of this file.
# Verify both keys reach default/smoke-secret after apply. Remove username
# from keys and apply: the same definition updates in place and sync_status
# reaches Synced before apply returns. Remove keys to deliver the whole entry.
# Remove the sync target and apply: wait for Withdrawing to disappear; the
# cluster copy goes shortly after. Destroy keeps the stored OpenBao values.
# Import by name and check a subsequent plan is empty with the same keys.

resource "kupe_secret" "smoke" {
  name        = "smoke-secret"
  secret_path = "smoke/test-credentials"

  sync = [
    {
      cluster   = kupe_cluster.smoke.name
      namespace = "default"
      keys      = ["username", "password"]
    },
  ]
}

output "smoke_secret_sync_status" {
  description = "Per-target delivery status after the current values have reached the namespace."
  value       = kupe_secret.smoke.sync_status
}
