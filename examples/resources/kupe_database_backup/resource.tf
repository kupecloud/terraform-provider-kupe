# Take a manual backup before a schema migration. It is kept until you delete
# it, even after the database is deleted. Set `name` when a restored database
# refers to the backup.
resource "kupe_database_backup" "before_migration" {
  database = kupe_database.orders.name
  name     = "orders-db-before-migration"
}
