data "kupe_app_image" "web" {
  image = "ghcr.io/kupecloud/hello-kupe:1.5.1"
}

output "image_reference" {
  description = "Immutable image reference to use as kupe_app_environment.web.image."
  value       = data.kupe_app_image.web.reference
}
