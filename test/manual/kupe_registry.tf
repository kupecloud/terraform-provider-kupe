# Smoke test for kupe_registry, kupe_registry_binding, kupe_registry_token
# and data.kupe_registry.
#
# Verifies:
#   - 201 from POST /registries; phase reaches Running; endpoint is
#     registry.kupe.cloud/<tenant>
#   - the connection: phase Ready, pull_username set; the push namespace
#     "default" gets the Secret kupe-registry-push; the create warns that
#     pods started earlier need a restart
#   - the token: phase Ready; `tofu state show kupe_registry_token.smoke`
#     shows secret as (sensitive value); docker login with registry_host,
#     username and the secret output succeeds
#   - destroy is refused while deletion_protection is true:
#       tofu destroy                                  # refused for the registry
#       tofu apply -var registry_deletion_protection=false
#       tofu destroy                                  # removes everything

variable "registry_deletion_protection" {
  description = "Deletion protection of the smoke registry. Set false and apply before destroying."
  type        = bool
  default     = true
}

resource "kupe_registry" "smoke" {
  storage_limit       = "1Gi"
  deletion_protection = var.registry_deletion_protection
}

resource "kupe_registry_binding" "smoke" {
  registry        = kupe_registry.smoke.name
  cluster         = kupe_cluster.smoke.name
  push_namespaces = ["default"]
}

resource "kupe_registry_token" "smoke" {
  registry        = kupe_registry.smoke.name
  display_name    = "smoke-test-token"
  access          = "pull"
  expires_in_days = 1
}

data "kupe_registry" "smoke_read" {
  name = kupe_registry.smoke.name
}

output "smoke_registry_endpoint" {
  description = "Image path prefix reported by the data source."
  value       = data.kupe_registry.smoke_read.endpoint
}

output "smoke_registry_token_login" {
  description = "Registry host and username for docker login."
  value = {
    host     = kupe_registry_token.smoke.registry_host
    username = kupe_registry_token.smoke.username
  }
}

output "smoke_registry_token_secret" {
  description = "Pass to docker login --password-stdin."
  value       = kupe_registry_token.smoke.secret
  sensitive   = true
}
