data "kupe_database" "orders" {
  name = "orders-db"
}

output "orders_db_running_version" {
  value = data.kupe_database.orders.current_version
}
