resource "kupe_app" "multi" {
  name = "shop-multi"

  # Before destroy, set this to false and apply.
  # deletion_protection = false
}

locals {
  app_environments = {
    staging    = { cluster = "europe", replicas = 1 }
    production = { cluster = "europe", replicas = 2 }
  }
}

resource "kupe_app_environment" "multi" {
  for_each = local.app_environments
  app      = kupe_app.multi.name
  name     = each.key
  cluster  = each.value.cluster

  web = {
    image    = "ghcr.io/kupecloud/hello-kupe@sha256:be393b04d81f3a3e63d57be12dce11e823b1af75ea3997eecbc2d19a3b434f93"
    port     = 8080
    replicas = each.value.replicas
  }

  # Before destroy, set this to false and apply.
  # deletion_protection = false
}
