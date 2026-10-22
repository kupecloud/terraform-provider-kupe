# The tenant's one registry. Images live at
# registry.kupe.cloud/<tenant>/<repository>:<tag>.
resource "kupe_registry" "this" {
  # Optional: stay below the plan's storage limit. Leave it out to use the
  # most the plan allows.
  storage_limit = "20Gi"

  # deletion_protection defaults to true: the platform refuses to delete the
  # registry, and every image in it, until it is set to false and applied.
}

output "registry_endpoint" {
  description = "Image path prefix, e.g. registry.kupe.cloud/acme."
  value       = kupe_registry.this.endpoint
}
