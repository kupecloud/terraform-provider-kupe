# The API reads and writes data with the app role.
resource "kupe_database_binding" "api" {
  database  = kupe_database.orders.name
  cluster   = kupe_cluster.production.name
  namespace = "api"
  role      = "app"
}

# Schema migrations run with the owner role, under their own Secret name.
resource "kupe_database_binding" "migrations" {
  database    = kupe_database.orders.name
  cluster     = kupe_cluster.production.name
  namespace   = "api"
  role        = "owner"
  secret_name = "orders-db-migrations"
}

output "orders_db_host" {
  description = "In-cluster DNS name the api namespace connects to."
  value       = kupe_database_binding.api.host
}
