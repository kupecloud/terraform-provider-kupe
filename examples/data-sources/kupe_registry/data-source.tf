data "kupe_registry" "this" {}

output "registry_storage_used_bytes" {
  value = data.kupe_registry.this.storage_used_bytes
}
