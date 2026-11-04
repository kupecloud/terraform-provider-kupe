---
page_title: "kupe_database_binding Resource - kupe"
subcategory: ""
description: |-
  Binds a Kupe database into namespaces of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into each listed namespace and opens the network path; other namespaces see neither. A binding lists 1 to 10 namespaces, and the bindings of one database list at most 20 between them. Create and Update wait for the current generation and every listed namespace to be Ready, and fail at once if one is Failed: it already has a Service or Endpoints object named after the database, or a Secret named secret_name, that the platform did not create. A binding that fails or times out on create stays in state, marked tainted. Delete waits until the binding is gone. Set timeouts with the timeouts attribute, for example timeouts = { create = "10m" } (defaults: create/delete 5m, update 10m).
  namespaces changes in place: a namespace added gets the Service and the Secret, and the namespaces kept work throughout. Enabling dedicated credentials changes in place; turning them off replaces the binding. Changing credentials.rotation_trigger requests a dedicated rotation. Other arguments except timeouts replace the binding. Removing a namespace, or the binding, removes the Secret there, and the Service and the network path too unless another binding of the same database still lists that namespace. With shared credentials, the password does not change: rotate the role's credentials if the namespace should lose knowledge of it.
---

# kupe_database_binding (Resource)

Binds a Kupe database into namespaces of one cluster for one role. The Kupe platform delivers a Service named after the database and a credentials Secret into each listed namespace and opens the network path; other namespaces see neither. A binding lists 1 to 10 namespaces, and the bindings of one database list at most 20 between them. Create and Update wait for the current generation and every listed namespace to be Ready, and fail at once if one is Failed: it already has a Service or Endpoints object named after the database, or a Secret named `secret_name`, that the platform did not create. A binding that fails or times out on create stays in state, marked tainted. Delete waits until the binding is gone. Set timeouts with the `timeouts` attribute, for example `timeouts = { create = "10m" }` (defaults: create/delete 5m, update 10m).

`namespaces` changes in place: a namespace added gets the Service and the Secret, and the namespaces kept work throughout. Enabling dedicated credentials changes in place; turning them off replaces the binding. Changing `credentials.rotation_trigger` requests a dedicated rotation. Other arguments except `timeouts` replace the binding. Removing a namespace, or the binding, removes the Secret there, and the Service and the network path too unless another binding of the same database still lists that namespace. With shared credentials, the password does not change: rotate the role's credentials if the namespace should lose knowledge of it.

## Example Usage

```terraform
# The API and its background jobs read and write data with the readwrite
# role. One binding delivers the Service and the Secret into both namespaces.
resource "kupe_database_binding" "api" {
  database    = kupe_database.orders.name
  cluster     = kupe_cluster.prod.name
  namespaces  = ["orders-api", "orders-worker"]
  role        = "readwrite"
  credentials = { dedicated = true }
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

# Grant a group role declared in kupe_database.orders.postgres.roles.
resource "kupe_database_binding" "custom_reports" {
  database   = kupe_database.orders.name
  cluster    = kupe_cluster.prod.name
  namespaces = ["sales-reports"]
  role       = "custom"
  role_name  = "sales_reports"
  credentials = {
    dedicated        = true
    rotation_trigger = "rotation-ticket-1"
  }
}
```

### Selecting a PostgreSQL database

Set `logical_database = "analytics"` to bind an added PostgreSQL database. Omit it for the first database, including a restored instance's preserved first name. Changing the selection replaces the binding.

The default binding resource name is `{database}-{cluster}-{role}` when the selection is omitted or equals the instance name with hyphens replaced by underscores. Other explicit selections use `{database}-{logical_database}-{cluster}-{role}`. For example, an instance `orders-db` restored with first PostgreSQL name `historical_db` gets `orders-db-prod-readwrite` when selection is omitted, or `orders-db-historical-db-prod-readwrite` when `historical_db` is selected explicitly. This provider resource-name rule is tool-specific. The default Secret name uses the effective first database: `{database}-{role}` for that database, or `{database}-{logical_database}-{role}` for an added database. Both restored examples therefore use Secret `orders-db-readwrite`. Custom bindings use `role_name` in place of `role`; underscores become hyphens. Namespaces never appear in the default name. Existing bindings keep their name when `logical_database` is omitted. If the logical selection is explicitly unknown during planning, the default name becomes known when the selection is known. Omitting the selection, or setting it to `null`, uses the first-database default during planning. Set `name` explicitly if you need a different name.

A taken binding or Secret name is refused. Set `name` or `secret_name` explicitly to choose another name. The provider does not retry with a generated name. The instance's cap of 20 bound namespaces covers all its PostgreSQL databases.

Use an owner binding to run grants inside an added database before using its readwrite, readonly or custom bindings. Ready confirms provisioning and Secret preparation; it does not verify tenant SQL grants. See [Logical databases](database.md#logical-databases).

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

### Dedicated credentials and rotation

Set `credentials = { dedicated = true }` to give every listed namespace its own login and password. Enabling dedicated credentials updates the binding in place, including when `namespaces` changes in the same apply. Turning dedicated credentials off replaces the binding. Owner bindings use shared credentials. Custom bindings require `role_name` and dedicated credentials.

For an omitted selection or an explicit selection equal to the normalised instance name, a custom role such as `sales_reports` gives default binding name `{database}-{cluster}-sales-reports`. An explicit restored first name that differs includes the logical-name component as described above. The default Secret name for the effective first database is `{database}-sales-reports`.

Set or change `credentials.rotation_trigger` to request one dedicated rotation on update. Removing the trigger does not rotate. The trigger is your own marker, such as a rotation ticket number; Terraform stores it in state. Passwords never reach the provider.

Update follows the rotation request token until the platform has applied it and prepared every listed namespace's credentials Secret. `observed_rotation` records the completed token and `namespace_status[].delivered_rotation` shows progress. If another rotation supersedes the request, Update follows the newer token. The copy into the cluster follows asynchronously and is not observed. Restart applications after delivery.

### When a binding waits or fails

A binding is `Pending` while its database or cluster does not exist yet, is not running yet or is being deleted, and while a namespace it lists is not delivered yet. Create and Update keep waiting until their timeout, then fail. The Kupe platform keeps trying, and the binding becomes `Ready` once every listed namespace is.

A namespace is `Failed` when it already has a Service or Endpoints object named after the database, or a Secret named `secret_name`, that the Kupe platform did not create. The binding does not replace that object, and the other namespaces keep working. Create and Update stop at once with an error that names the namespace, the database and the Secret name, and `namespace_status` carries the platform's message for it.

- After a failed Create, Terraform keeps the binding in state, marked tainted: rename or remove the object, or set a different `secret_name`, then run `terraform apply` to replace the binding. `terraform destroy` removes it.
- After a failed Update, the new list is in state. Rename or remove the object and the platform delivers into the namespace on its next try, or take the namespace out of `namespaces` and apply.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `cluster` (String) Cluster to bind into. It may not exist yet: the binding becomes Ready once it does, so Create waits up to its timeout and then fails.
- `database` (String) Name of the database to bind.
- `namespaces` (Set of String) Namespaces inside the cluster that receive the Service and credentials Secret: 1 to 10, none `kube-*` or `argocd`. Changes in place.
- `role` (String) Role granted: `owner` (can change the schema), `readwrite` (reads and writes data), `readonly` (reads data), or `custom` (a declared role).

### Optional

- `credentials` (Attributes) Dedicated credentials give every namespace its own login and password. Passwords are never returned to Terraform. (see [below for nested schema](#nestedatt--credentials))
- `logical_database` (String) PostgreSQL database to bind. Omit it to use the first database, including a restored instance's preserved first name. Changing it replaces the binding. Added databases include their logical name in default Secret names. See `name` for the binding resource name. If a default name is taken, set `name` or `secret_name` explicitly.
- `name` (String) Binding name. Defaults to `{database}-{cluster}-{role}` when `logical_database` is omitted or equals the instance name with hyphens replaced by underscores. Other explicit selections use `{database}-{logical_database}-{cluster}-{role}`, including a restored first database whose name differs from the normalised instance name. Uses `role_name` for custom and replaces underscores with hyphens. If `logical_database` is explicitly unknown during planning, the default name becomes known when the selection is known. Set `name` explicitly if the default is taken, longer than 63 characters, or you need a second binding of the same role into the same cluster.
- `role_name` (String) Declared role granted by a custom binding. Required for custom and refused for built-in roles. Changing it replaces the binding.
- `secret_name` (String) Name of the credentials Secret in every listed namespace. When unset, a new binding uses `{database}-{role}` for the first PostgreSQL database or `{database}-{logical_database}-{role}` for an added database (using `role_name` for custom, with underscores replaced by hyphens). A taken default is refused; set `secret_name` explicitly. An existing or imported binding keeps its current name, so removing the attribute from configuration does not change the name back: set it to `{database}-{role}` instead. A different name replaces the binding. The Secret holds `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`; mount it at `/bindings/<secret_name>`, where `uri` expects `ca.crt`.
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))

### Read-Only

- `etag` (String) Resource version used for optimistic locking during updates.
- `namespace_status` (Attributes List) What the binding delivered into each namespace, sorted by name. A namespace removed from `namespaces` stays listed until its Service and Secret are withdrawn. (see [below for nested schema](#nestedatt--namespace_status))
- `observed_rotation` (String) Dedicated rotation token applied and delivered to every listed namespace; empty for shared credentials.
- `phase` (String) Current phase: Pending (waiting for the database, the cluster or a namespace), Ready (every listed namespace is Ready), or Failed (a listed namespace already has a Service, Endpoints object or Secret of the same name that the platform did not create).

<a id="nestedatt--credentials"></a>
### Nested Schema for `credentials`

Optional:

- `dedicated` (Boolean) Use a separate login and password for each namespace. False to true changes in place; true to false replaces the binding. Owner bindings use shared credentials; custom bindings require dedicated credentials.
- `rotation_trigger` (String) Set or change this value to rotate dedicated credentials on update. Removing it does not rotate. This is an arbitrary trigger value, not a password. Terraform keeps the value in state. Restart applications after delivery.


<a id="nestedatt--timeouts"></a>
### Nested Schema for `timeouts`

Optional:

- `create` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).
- `delete` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours). Setting a timeout for a Delete operation is only applicable if changes are saved into state before the destroy operation occurs.
- `update` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).


<a id="nestedatt--namespace_status"></a>
### Nested Schema for `namespace_status`

Read-Only:

- `delivered_rotation` (String) Rotation token carried by the credentials Secret delivered for this namespace.
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
