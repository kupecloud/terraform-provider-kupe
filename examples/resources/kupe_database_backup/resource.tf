# Take a manual backup before a schema migration. It is kept until you delete
# it, even after the database is deleted.
resource "kupe_database_backup" "before_migration" {
  database = kupe_database.orders.name
  name     = "orders-db-before-migration"
}
