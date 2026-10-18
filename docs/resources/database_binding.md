---
page_title: "kupe_database_binding Resource - kupe"
subcategory: ""
description: |-
  Binds a Kupe database into namespaces of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into each listed namespace and opens the network path; other namespaces see neither. A binding lists 1 to 10 namespaces, and the bindings of one database list at most 20 between them. Create and Update wait until every listed namespace is Ready, and fail at once if one is Failed: it already has a Service or Endpoints object named after the database, or a Secret named secret_name, that the platform did not create. A binding that fails on create stays in state, marked tainted. Delete waits until the binding is gone. Set timeouts with the timeouts attribute, for example timeouts = { create = "10m" } (defaults: create/update/delete 5m).
  namespaces changes in place: a namespace added gets the Service and the Secret, and the namespaces kept work throughout. Changing any other argument except timeouts replaces the binding. Removing a namespace, or the binding, removes the Secret there, and the Service and the network path too unless another binding of the same database still lists that namespace. The password does not change: rotate the role's credentials if the namespace should lose knowledge of it.
---

# kupe_database_binding (Resource)

Binds a Kupe database into namespaces of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into each listed namespace and opens the network path; other namespaces see neither. A binding lists 1 to 10 namespaces, and the bindings of one database list at most 20 between them. Create and Update wait until every listed namespace is Ready, and fail at once if one is Failed: it already has a Service or Endpoints object named after the database, or a Secret named `secret_name`, that the platform did not create. A binding that fails on create stays in state, marked tainted. Delete waits until the binding is gone. Set timeouts with the `timeouts` attribute, for example `timeouts = { create = "10m" }` (defaults: create/update/delete 5m).

`namespaces` changes in place: a namespace added gets the Service and the Secret, and the namespaces kept work throughout. Changing any other argument except `timeouts` replaces the binding. Removing a namespace, or the binding, removes the Secret there, and the Service and the network path too unless another binding of the same database still lists that namespace. The password does not change: rotate the role's credentials if the namespace should lose knowledge of it.

## Example Usage

```terraform
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
```

### Using the credentials Secret

Inside the cluster, each bound namespace sees a Service named after the database and the Secret named by `secret_name`, with the keys `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`. `type` is `postgresql` and `provider` is `kupe`.

Mount the Secret as a volume at `/bindings/<secret_name>` and set `SERVICE_BINDING_ROOT=/bindings`. `uri` is a `postgresql://` connection string with `sslmode=verify-full` and `sslrootcert=/bindings/<secret_name>/ca.crt`, so it only connects from that mount path. Mounted elsewhere, build the connection from `host`, `port`, `database`, `username`, `password`, `sslmode` and `ca.crt` instead.

The example below is illustrative. It uses the [hashicorp/kubernetes](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs) provider, configured against the bound cluster, to mount the Secret into a Deployment in `orders-api`.

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
    namespace = "orders-api"
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
```

Have workloads read the Secret themselves. Reading it with a `kubernetes_secret_v1` data source puts the password in Terraform state.

### Changing the namespaces

`namespaces` changes in place. Terraform sends the complete new list: a namespace added gets the Service and the Secret, a namespace taken out loses its Secret (and the Service and network path, unless another binding of the database lists it), and the namespaces in both lists keep working throughout. Update waits until the platform has delivered the new list, which includes withdrawing from the namespaces taken out.

A binding lists at most 10 namespaces, which the plan checks. The bindings of one database list at most 20 namespaces between them, and a namespace can be in only one binding per cluster and role of a database. The Kupe API refuses a change that breaks either rule when it is applied; a change that only takes namespaces out never breaks them.

### When a binding waits or fails

A binding is `Pending` while its database or cluster does not exist yet, is not running yet or is being deleted, and while a namespace it lists is not delivered yet. Create and Update keep waiting until their timeout, then warn. The Kupe platform keeps trying, and the binding becomes `Ready` once every listed namespace is.

A namespace is `Failed` when it already has a Service or Endpoints object named after the database, or a Secret named `secret_name`, that the Kupe platform did not create. The binding does not replace that object, and the other namespaces keep working. Create and Update stop at once with an error that names the namespace, the database and the Secret name, and `namespace_status` carries the platform's message for it.

- After a failed Create, Terraform keeps the binding in state, marked tainted: rename or remove the object, or set a different `secret_name`, then run `terraform apply` to replace the binding. `terraform destroy` removes it.
- After a failed Update, the new list is in state. Rename or remove the object and the platform delivers into the namespace on its next try, or take the namespace out of `namespaces` and apply.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `cluster` (String) Cluster to bind into. It may not exist yet: the binding becomes Ready once it does, so Create waits up to its timeout and then warns.
- `database` (String) Name of the database to bind.
- `namespaces` (Set of String) Namespaces inside the cluster that receive the Service and credentials Secret: 1 to 10, none `kube-*` or `argocd`. Changes in place.
- `role` (String) Role granted: `owner` (can change the schema), `readwrite` (reads and writes data) or `readonly` (reads data).

### Optional

- `name` (String) Binding name. Defaults to `{database}-{cluster}-{role}`. Set it explicitly if that is longer than 63 characters, or to bind the same role of a database into the same cluster with a second binding.
- `secret_name` (String) Name of the credentials Secret in every listed namespace. When unset, a new binding uses `{database}-{role}` and an existing or imported binding keeps its current name, so removing the attribute from configuration does not change the name back: set it to `{database}-{role}` instead. A different name replaces the binding. The Secret holds `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`; mount it at `/bindings/<secret_name>`, where `uri` expects `ca.crt`.
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))

### Read-Only

- `etag` (String) Resource version used for optimistic locking during updates.
- `namespace_status` (Attributes List) What the binding delivered into each namespace, sorted by name. A namespace removed from `namespaces` stays listed until its Service and Secret are withdrawn. (see [below for nested schema](#nestedatt--namespace_status))
- `phase` (String) Current phase: Pending (waiting for the database, the cluster or a namespace), Ready (every listed namespace is Ready), or Failed (a listed namespace already has a Service, Endpoints object or Secret of the same name that the platform did not create).

<a id="nestedatt--timeouts"></a>
### Nested Schema for `timeouts`

Optional:

- `create` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).
- `delete` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours). Setting a timeout for a Delete operation is only applicable if changes are saved into state before the destroy operation occurs.
- `update` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).


<a id="nestedatt--namespace_status"></a>
### Nested Schema for `namespace_status`

Read-Only:

- `host` (String) In-cluster DNS name applications in this namespace connect to, e.g. `orders-db.orders-api.svc.cluster.local`. Empty until the Service is delivered.
- `message` (String) Why the namespace is not Ready; empty when it is.
- `name` (String) Namespace inside the cluster.
- `phase` (String) Pending, Ready, or Failed (the namespace already has a Service, Endpoints object or Secret of the same name that the platform did not create).

## Import

Import is supported using the following syntax:

```shell
# <database>/<binding>
terraform import kupe_database_binding.api orders-db/orders-db-prod-readwrite
```
