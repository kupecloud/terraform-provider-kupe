# An admin token for CI on the cluster "dev"; referencing the cluster orders
# this create after it. Its token and kubeconfig are in Terraform state: keep
# state in an encrypted backend with restricted access.
resource "kupe_cluster_access_token" "ci" {
  cluster      = kupe_cluster.dev.name
  display_name = "GitHub Actions"
  access = {
    role = "admin"
  }
  expires_in_days = 30
}

output "ci_kubeconfig" {
  description = "Kubeconfig for CI. Store it in your CI system's secret store."
  value       = kupe_cluster_access_token.ci.kubeconfig
  sensitive   = true
}
