resource "kupe_cluster" "production" {
  name         = "production"
  display_name = "Production"
  type         = "shared"
  version      = "1.32"

  resources = {
    cpu     = "4"
    memory  = "16Gi"
    storage = "100Gi"
  }
}
