resource "kupe_database" "orders" {
  name    = "orders-db"
  version = "18"

  postgres = {
    databases = [{ name = "orders_db" }, { name = "analytics" }]
  }

  resources = {
    cpu     = "500m"
    memory  = "1Gi"
    storage = "10Gi"
  }
}

resource "kupe_app" "orders" {
  name = "orders"
}

resource "kupe_app_environment" "orders" {
  app     = kupe_app.orders.name
  name    = "production"
  cluster = "europe"

  web = {
    image = "ghcr.io/kupecloud/hello-kupe@sha256:be393b04d81f3a3e63d57be12dce11e823b1af75ea3997eecbc2d19a3b434f93"
    port  = 8080
  }

  databases = [
    # The URL of the first PostgreSQL database arrives in DATABASE_URL.
    { name = "main", database = kupe_database.orders.name },
    { name = "reports", database = kupe_database.orders.name, logical_database = "analytics", env = "REPORTS_URL" },
  ]
}
