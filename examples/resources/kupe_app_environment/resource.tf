resource "kupe_app" "this" {
  name = "shop"
}

resource "kupe_app_environment" "this" {
  app     = kupe_app.this.name
  name    = "production"
  cluster = "europe"

  web = {
    image = "ghcr.io/kupecloud/hello-kupe@sha256:be393b04d81f3a3e63d57be12dce11e823b1af75ea3997eecbc2d19a3b434f93"
    port  = 8080
    env = [{ name = "LOG_LEVEL", value = "info" }]
    health_checks = { readiness = { http = { path = "/healthz" } } }
  }

  # Write values at this path in the console Vault page before referencing
  # them in web.env. Do not target this environment's namespace separately.
  # secrets = [{ name = "payments", path = "apps/shop/production/payments" }]

  # Increase by one to retry an unchanged deployment.
  # deployment_revision = 2

  # Before destroy, set this to false and apply.
  # deletion_protection = false
}
