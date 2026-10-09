resource "kupe_database" "billing" {
  name = "billing-db"

  resources = {
    cpu     = "1"
    memory  = "2Gi"
    storage = "50Gi"
  }

  # Stop Terraform from planning a destroy at all, on top of the platform's
  # deletion protection.
  lifecycle {
    prevent_destroy = true
  }
}
