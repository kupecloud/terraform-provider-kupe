# Illustrative: consume the binding from inside the cluster with the
# hashicorp/kubernetes provider, configured against the bound cluster.

# Workloads should reference the Secret directly so the password never
# passes through Terraform.
resource "kubernetes_deployment_v1" "api" {
  metadata {
    name      = "api"
    namespace = kupe_database_binding.api.namespace
  }

  spec {
    selector {
      match_labels = { app = "api" }
    }

    template {
      metadata {
        labels = { app = "api" }
      }

      spec {
        container {
          name  = "api"
          image = "ghcr.io/example/api:1.0.0"

          env {
            name = "DATABASE_URL"
            value_from {
              secret_key_ref {
                name = kupe_database_binding.api.secret_name
                key  = "uri"
              }
            }
          }
        }
      }
    }
  }
}

# When another Terraform resource needs a value, read the Secret in the bound
# namespace. Its values are then stored in Terraform state.
data "kubernetes_secret_v1" "orders_db" {
  metadata {
    name      = kupe_database_binding.api.secret_name
    namespace = kupe_database_binding.api.namespace
  }
}

output "orders_db_port" {
  value     = data.kubernetes_secret_v1.orders_db.data["port"]
  sensitive = true
}
