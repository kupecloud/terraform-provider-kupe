data "kupe_app_image" "app" {
  image = "ghcr.io/kupecloud/hello-kupe:1.5.1"
}

output "image_reference" {
  description = "Immutable image reference for kupe_app_environment.runtime.image."
  value       = data.kupe_app_image.app.reference
}
