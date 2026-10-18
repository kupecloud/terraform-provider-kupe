# The API reads and writes data with the readwrite role.
resource "kupe_database_binding" "api" {
  database  = kupe_database.orders.name
  cluster   = kupe_cluster.production.name
  namespace = "api"
  role      = "readwrite"
}

# Schema migrations run with the owner role in a namespace of their own, so no
# pod in api can mount the owner credentials.
resource "kupe_database_binding" "migrations" {
  database    = kupe_database.orders.name
  cluster     = kupe_cluster.production.name
  namespace   = "api-migrations"
  role        = "owner"
  secret_name = "orders-db-migrations"
}

# A reporting job reads data and cannot change it.
resource "kupe_database_binding" "reports" {
  database  = kupe_database.orders.name
  cluster   = kupe_cluster.production.name
  namespace = "reports"
  role      = "readonly"
}

output "orders_db_host" {
  description = "In-cluster DNS name the api namespace connects to."
  value       = kupe_database_binding.api.host
}
