resource "kupe_database" "orders" {
  postgres = {
    databases = [
      { name = "orders_db" },
      { name = "analytics", schemas = [{ name = "reporting" }] }
    ]
    roles      = [{ name = "sales_reports" }]
    extensions = [{ name = "pgcrypto" }]
  }
  name    = "orders-db"
  version = "18"

  resources = {
    cpu     = "500m"
    memory  = "1Gi"
    storage = "10Gi"
  }

  # Two instances with a synchronous standby. Can be turned on or off later.
  high_availability = true

  # deletion_protection defaults to true: the platform refuses to delete the
  # database until it is set to false and applied.
}

resource "kupe_database_binding" "analytics" {
  database         = kupe_database.orders.name
  logical_database = "analytics"
  cluster          = "prod"
  namespaces       = ["reports"]
  role             = "readwrite"
}
