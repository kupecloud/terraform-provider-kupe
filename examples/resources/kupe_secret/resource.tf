resource "kupe_secret" "app_config" {
  name        = "app-config"
  secret_path = "apps/app-config"

  sync = [
    {
      cluster   = "production"
      namespace = "hello"
    },
    {
      cluster   = "staging"
      namespace = "hello"
    },
  ]
}
