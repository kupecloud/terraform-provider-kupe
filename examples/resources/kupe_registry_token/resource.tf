# A push token for CI. Its secret is in Terraform state: keep state in an
# encrypted backend with restricted access.
resource "kupe_registry_token" "ci" {
  display_name    = "GitHub Actions"
  access          = "push"
  expires_in_days = 30
}

output "registry_login" {
  description = "Pass the secret to docker login --password-stdin."
  value = {
    host     = kupe_registry_token.ci.registry_host
    username = kupe_registry_token.ci.username
  }
}
