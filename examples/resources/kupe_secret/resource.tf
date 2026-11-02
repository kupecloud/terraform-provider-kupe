# Use OpenTofu 1.11 or Terraform 1.11.1 or later.
variable "app_config_values" {
  description = "Complete values to store and deliver. Supply them securely when planning and applying."
  type        = map(string)
  sensitive   = true
  ephemeral   = true
}

resource "kupe_secret" "app_config" {
  name        = "app-config"
  secret_path = "apps/app-config"

  values_wo         = var.app_config_values
  values_wo_version = 1

  sync = [
    {
      cluster   = "prod"
      namespace = "hello"
      keys      = ["username", "password"]
    },
    {
      cluster   = "staging"
      namespace = "hello"
    },
  ]
}
