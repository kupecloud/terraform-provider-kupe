# Use OpenTofu 1.11 or Terraform 1.11.1 or later.
variable "app_config_values" {
  description = "Every value to store and deliver. Pass them in at plan and apply time, never in a file."
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
