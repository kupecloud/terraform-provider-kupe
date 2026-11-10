# Step 1: create the database on PostgreSQL 17.
resource "kupe_database" "orders" {
  name    = "orders-db"
  version = "17"

  resources = {
    cpu     = "1"
    memory  = "2Gi"
    storage = "50Gi"
  }

  # A large database takes longer than the 30 minute default to back up and
  # upgrade.
  timeouts = {
    update = "60m"
  }
}

# Step 2: raise `version` to "18" and run `terraform apply`. The apply updates
# the database in place and returns once it runs PostgreSQL 18. To cancel an
# upgrade that has not started, was rolled back or failed, set `version` back
# to "17" and apply.
