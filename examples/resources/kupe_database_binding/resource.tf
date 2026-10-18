# The API and its background jobs read and write data with the readwrite
# role. One binding delivers the Service and the Secret into both namespaces.
resource "kupe_database_binding" "api" {
  database   = kupe_database.orders.name
  cluster    = kupe_cluster.prod.name
  namespaces = ["orders-api", "orders-worker"]
  role       = "readwrite"
}

# Schema migrations run with the owner role in a namespace of their own, so no
# pod in orders-api can mount the owner credentials.
resource "kupe_database_binding" "migrations" {
  database   = kupe_database.orders.name
  cluster    = kupe_cluster.prod.name
  namespaces = ["orders-migrate"]
  role       = "owner"
}

# A reporting job reads data and cannot change it.
resource "kupe_database_binding" "reports" {
  database   = kupe_database.orders.name
  cluster    = kupe_cluster.prod.name
  namespaces = ["orders-reports"]
  role       = "readonly"
}

output "orders_db_hosts" {
  description = "In-cluster DNS name each namespace of the api binding connects to."
  value       = { for ns in kupe_database_binding.api.namespace_status : ns.name => ns.host }
}
