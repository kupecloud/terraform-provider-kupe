# Engineer-run smoke only, after the App runtime is released and available.
# The digest comes from hello-kupe/chart/values.yaml at source preparation.
# Exercise create, image/settings update, retry, import, protection and destroy.
# Verify first-deploy failures keep untainted state and unchanged plans are empty.
# This fixture has not been applied by the implementation agent.
resource "kupe_app" "smoke" {
  name                = "smoke-app"
  deletion_protection = false
}

resource "kupe_app_environment" "smoke" {
  app                 = kupe_app.smoke.name
  name                = "staging"
  cluster             = kupe_cluster.smoke.name
  deletion_protection = false

  web = {
    image = "ghcr.io/kupecloud/hello-kupe@sha256:be393b04d81f3a3e63d57be12dce11e823b1af75ea3997eecbc2d19a3b434f93"
    port  = 8080
  }
}

output "smoke_app_address" {
  description = "Address serving the smoke app after its requested release succeeds."
  value       = kupe_app_environment.smoke.address
}
