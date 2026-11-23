# Smoke test for kupe_cluster_access_token. Needs kupe_cluster.smoke Running.
#
# Verifies:
#   - 201 from POST /clusters/smoke-cluster/access-tokens; Create waits for
#     phase Ready and collects once: phase Ready, collected true
#   - `tofu state show kupe_cluster_access_token.smoke` shows token and
#     kubeconfig as (sensitive value); a second plan is empty
#   - the kubeconfig works:
#       tofu output -raw smoke_cluster_access_kubeconfig > /tmp/cat.kubeconfig
#       KUBECONFIG=/tmp/cat.kubeconfig kubectl get ns
#   - destroy revokes it: after `tofu destroy -target=kupe_cluster_access_token.smoke`
#     the same kubectl command is refused (401) within a minute

resource "kupe_cluster_access_token" "smoke" {
  cluster      = kupe_cluster.smoke.name
  display_name = "smoke-test-cluster-token"
  access = {
    role = "admin"
  }
  expires_in_days = 1
}

output "smoke_cluster_access_kubeconfig" {
  description = "Kubeconfig of the smoke cluster access token."
  value       = kupe_cluster_access_token.smoke.kubeconfig
  sensitive   = true
}
