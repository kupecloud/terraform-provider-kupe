# Smoke test for kupe_secret.
#
# Targets a (likely non-existent) cluster on purpose — we're exercising
# the API contract for the managed secret CRD, not the operator's
# downstream sync. The status.phase will sit at Pending or Failed
# depending on whether you also applied kupe_cluster.smoke. Either way
# the API contract surface is what we're validating here.

resource "kupe_secret" "smoke" {
  name        = "smoke-secret"
  secret_path = "smoke/test-credentials"

  sync = [
    {
      cluster   = "smoke-cluster"
      namespace = "default"
    },
  ]
}
