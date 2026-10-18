---
page_title: "kupe_database_binding Resource - kupe"
subcategory: ""
description: |-
  Binds a Kupe database into one namespace of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into that namespace and opens the network path; other namespaces see neither. Create waits for phase=Ready, and fails at once if the binding is Failed: the namespace already has a Service named after the database, or a Secret named secret_name, that the platform did not create. The failed binding stays in state, marked tainted. Delete waits until the binding is gone. Set timeouts with the timeouts attribute, for example timeouts = { create = "10m" } (defaults: create/delete 5m).
  Bindings cannot be changed: changing any argument replaces the binding. Removing a binding removes the Service, the Secret and the network path, but not knowledge of the password; rotate the role's credentials if that matters.
---

# kupe_database_binding (Resource)

Binds a Kupe database into one namespace of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into that namespace and opens the network path; other namespaces see neither. Create waits for phase=Ready, and fails at once if the binding is Failed: the namespace already has a Service named after the database, or a Secret named `secret_name`, that the platform did not create. The failed binding stays in state, marked tainted. Delete waits until the binding is gone. Set timeouts with the `timeouts` attribute, for example `timeouts = { create = "10m" }` (defaults: create/delete 5m).

Bindings cannot be changed: changing any argument replaces the binding. Removing a binding removes the Service, the Secret and the network path, but not knowledge of the password; rotate the role's credentials if that matters.

## Example Usage

```terraform
# The API reads and writes data with the readwrite role.
resource "kupe_database_binding" "api" {
  database  = kupe_database.orders.name
  cluster   = kupe_cluster.production.name
  namespace = "api"
  role      = "readwrite"
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

# A reporting job reads data and cannot change it.
resource "kupe_database_binding" "reports" {
  database  = kupe_database.orders.name
  cluster   = kupe_cluster.production.name
  namespace = "reports"
  role      = "readonly"
}

output "orders_db_host" {
  description = "In-cluster DNS name the api namespace connects to."
  value       = kupe_database_binding.api.host
}
```

### Using the credentials Secret

Inside the cluster, the bound namespace sees a Service named after the database and the Secret named by `secret_name`, with the keys `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`. `type` is `postgresql` and `provider` is `kupe`.

Mount the Secret as a volume at `/bindings/<secret_name>` and set `SERVICE_BINDING_ROOT=/bindings`. `uri` is a `postgresql://` connection string with `sslmode=verify-full` and `sslrootcert=/bindings/<secret_name>/ca.crt`, so it only connects from that mount path. Mounted elsewhere, build the connection from `host`, `port`, `database`, `username`, `password`, `sslmode` and `ca.crt` instead.

The example below is illustrative. It uses the [hashicorp/kubernetes](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs) provider, configured against the bound cluster, to mount the Secret into a Deployment and to read the Secret from the bound namespace.

```terraform
# Illustrative: consume the binding from inside the cluster with the
# hashicorp/kubernetes provider, configured against the bound cluster.

# Workloads mount the Secret at /bindings/<secret_name> and set
# SERVICE_BINDING_ROOT=/bindings. The uri key's sslrootcert points at the
# mounted ca.crt, so uri only connects from that path. The password never
# passes through Terraform, and the mounted files update in place after a
# credential rotation.
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

          # The application reads $SERVICE_BINDING_ROOT/<secret_name>/uri.
          env {
            name  = "SERVICE_BINDING_ROOT"
            value = "/bindings"
          }

          volume_mount {
            name       = "database"
            mount_path = "/bindings/${kupe_database_binding.api.secret_name}"
            read_only  = true
          }
        }

        volume {
          name = "database"
          secret {
            secret_name = kupe_database_binding.api.secret_name
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
- `role` (String) Role granted: `owner` (can change the schema), `readwrite` (reads and writes data) or `readonly` (reads data).

### Optional

- `name` (String) Binding name. Defaults to `{database}-{cluster}-{namespace}-{role}`; set it explicitly if that is longer than 63 characters.
- `secret_name` (String) Name of the credentials Secret in the namespace. When unset, a new binding uses `{database}-{role}` and an existing or imported binding keeps its current name, so removing the attribute from configuration does not change the name back: set it to `{database}-{role}` instead. A different name replaces the binding. The Secret holds `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`; mount it at `/bindings/<secret_name>`, where `uri` expects `ca.crt`.
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
terraform import kupe_database_binding.api orders-db/orders-db-production-api-readwrite
```
