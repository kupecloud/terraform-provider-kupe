resource "kupe_secret" "app_config" {
  name        = "app-config"
  secret_path = "apps/app-config"

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
