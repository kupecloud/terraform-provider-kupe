data "kupe_app_image" "web" {
  image = "ghcr.io/kupecloud/hello-kupe:1.5.1"
}

resource "kupe_app" "this" {
  name = "shop"
}

resource "kupe_app_environment" "this" {
  app     = kupe_app.this.name
  name    = "production"
  cluster = "europe"

  web = {
    image = data.kupe_app_image.web.reference
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
