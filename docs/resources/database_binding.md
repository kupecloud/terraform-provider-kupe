---
page_title: "kupe_database_binding Resource - kupe"
subcategory: ""
description: |-
  Binds a Kupe database into one namespace of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into that namespace and opens the network path; other namespaces see neither. Create waits for phase=Ready, and fails at once if the binding is Failed: the namespace already has a Service named after the database, or a Secret named secret_name, that the platform did not create. The failed binding stays in state, marked tainted. Delete waits until the binding is gone. Timeouts are configurable via the timeouts block (defaults: create/delete 5m).
  Bindings cannot be changed: changing any argument replaces the binding. Removing a binding removes the Service, the Secret and the network path, but not knowledge of the password; rotate the role's credentials if that matters.
---

# kupe_database_binding (Resource)

Binds a Kupe database into one namespace of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into that namespace and opens the network path; other namespaces see neither. Create waits for phase=Ready, and fails at once if the binding is Failed: the namespace already has a Service named after the database, or a Secret named `secret_name`, that the platform did not create. The failed binding stays in state, marked tainted. Delete waits until the binding is gone. Timeouts are configurable via the `timeouts` block (defaults: create/delete 5m).

Bindings cannot be changed: changing any argument replaces the binding. Removing a binding removes the Service, the Secret and the network path, but not knowledge of the password; rotate the role's credentials if that matters.

## Example Usage

```terraform
# The API reads and writes data with the app role.
resource "kupe_database_binding" "api" {
  database  = kupe_database.orders.name
  cluster   = kupe_cluster.production.name
  namespace = "api"
  role      = "app"
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

output "orders_db_host" {
  description = "In-cluster DNS name the api namespace connects to."
  value       = kupe_database_binding.api.host
}
```

### Using the credentials Secret

Inside the cluster, the bound namespace sees a Service named after the database and the Secret named by `secret_name`, with the keys `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`.

The example below is illustrative. It uses the [hashicorp/kubernetes](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs) provider, configured against the bound cluster, to pass the connection URI to a Deployment and to read the Secret from the bound namespace.

```terraform
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
```

### When a binding waits or fails

A binding is `Pending` while its database or cluster does not exist yet, is not running yet or is being deleted. Create keeps waiting until its timeout, then warns. The Kupe platform keeps trying, and the binding becomes `Ready` once both are running.

A binding is `Failed` when the namespace already has a Service named after the database, or a Secret named `secret_name`, that the Kupe platform did not create. The binding does not replace that object. Create stops at once with an error that names both. Terraform keeps the failed binding in state, marked tainted: rename or remove the object, or set a different `secret_name`, then run `terraform apply` to replace the binding. `terraform destroy` removes it.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `cluster` (String) Cluster to bind into. It may not exist yet: the binding becomes Ready once it does, so Create waits up to its timeout and then warns.
- `database` (String) Name of the database to bind.
- `namespace` (String) Namespace inside the cluster that receives the Service and credentials Secret. Not `kube-*` or `argocd`.
- `role` (String) Role granted: `owner` (can change the schema) or `app` (reads and writes data).

### Optional

- `name` (String) Binding name. Defaults to `{database}-{cluster}-{namespace}-{role}`; set it explicitly if that is longer than 63 characters.
- `secret_name` (String) Name of the credentials Secret in the namespace. Defaults to `{database}-{role}`. The Secret holds `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`.
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))

### Read-Only

- `host` (String) In-cluster DNS name applications connect to, e.g. `orders-db.api.svc.cluster.local`.
- `phase` (String) Current phase: Pending (waiting for the database or the cluster), Ready, or Failed (the namespace already has a Service or Secret of the same name that the platform did not create).

<a id="nestedatt--timeouts"></a>
### Nested Schema for `timeouts`

Optional:

- `create` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).
- `delete` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours). Setting a timeout for a Delete operation is only applicable if changes are saved into state before the destroy operation occurs.

## Import

Import is supported using the following syntax:

```shell
# <database>/<binding>
terraform import kupe_database_binding.api orders-db/orders-db-production-api-app
```
