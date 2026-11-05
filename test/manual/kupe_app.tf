# Engineer-run smoke only, after the App runtime is released and available.
# This fixture pins the hello-kupe 1.5.1 image release.
# Confirm the resolved digest in the plan before applying.
# Exercise create, image/settings update, retry, import, protection and destroy.
# Verify first-deploy failures keep untainted state and unchanged plans are empty.
# This fixture has not been applied by the implementation agent.
data "kupe_app_image" "smoke" {
  image = "ghcr.io/kupecloud/hello-kupe:1.5.1"
}

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
    image = data.kupe_app_image.smoke.reference
    port  = 8080
  }
}

output "smoke_app_address" {
  description = "Address serving the smoke app after its requested release succeeds."
  value       = kupe_app_environment.smoke.address
}
