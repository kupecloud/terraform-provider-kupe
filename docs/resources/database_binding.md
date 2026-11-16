---
page_title: "kupe_database_binding Resource - kupe"
subcategory: ""
description: |-
  Gives namespaces in one cluster access to a Kupe database, with one role. The platform puts a Service named after the database and a credentials Secret in each listed namespace and opens the network path; other namespaces get neither. A binding lists 1 to 10 namespaces, and all bindings of one database together list at most 20. Create and Update fail straight away if a namespace is Failed because it already has a Service or Endpoints object named after the database, or a Secret named secret_name, that the platform did not create.
  Changes to namespaces apply in place: an added namespace gets the Service and the Secret, and the namespaces you keep work throughout. Turning dedicated credentials on is an in-place change, and turning them off replaces the binding. Changing credentials.rotation_trigger requests a dedicated rotation. Changing any other argument except timeouts replaces the binding. Removing a namespace, or the binding, removes the Secret there, and the Service and network path too unless another binding of the same database still lists that namespace. With shared credentials the password does not change, so rotate the role's credentials if that namespace should no longer know it.
---

# kupe_database_binding (Resource)

Gives namespaces in one cluster access to a Kupe database, with one role. The platform puts a Service named after the database and a credentials Secret in each listed namespace and opens the network path; other namespaces get neither. A binding lists 1 to 10 namespaces, and all bindings of one database together list at most 20. Create and Update fail straight away if a namespace is `Failed` because it already has a Service or Endpoints object named after the database, or a Secret named `secret_name`, that the platform did not create.

Changes to `namespaces` apply in place: an added namespace gets the Service and the Secret, and the namespaces you keep work throughout. Turning dedicated credentials on is an in-place change, and turning them off replaces the binding. Changing `credentials.rotation_trigger` requests a dedicated rotation. Changing any other argument except `timeouts` replaces the binding. Removing a namespace, or the binding, removes the Secret there, and the Service and network path too unless another binding of the same database still lists that namespace. With shared credentials the password does not change, so rotate the role's credentials if that namespace should no longer know it.

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

### Select a PostgreSQL database

Set `logical_database = "analytics"` to bind a PostgreSQL database you added to the instance. Leave it out to bind the first database, which for a restored instance is the first name it kept from its source. Changing the selection replaces the binding.

#### Default names

When you leave out `name` or `secret_name`, they default to:

- binding name: `{database}-{cluster}-{role}` when `logical_database` is left out or matches the instance name with hyphens replaced by underscores, and `{database}-{logical_database}-{cluster}-{role}` for any other selection. The API has no default binding name, so this rule is the provider's own;
- Secret name: `{database}-{role}` for the instance's first database, or `{database}-{logical_database}-{role}` for an added database.

A custom binding uses `role_name` in place of `role`, with underscores turned into hyphens. Namespaces never appear in a default name.

For example, an instance `orders-db` restored with the first PostgreSQL name `historical_db` gets the binding name `orders-db-prod-readwrite` when `logical_database` is left out, and `orders-db-historical-db-prod-readwrite` when `historical_db` is set explicitly. Both use the Secret `orders-db-readwrite`.

An existing binding keeps its name when `logical_database` is left out. Leaving out the selection, or setting it to `null`, plans the first-database default. If the selection is unknown during planning, the default name is known once the selection is. Set `name` if you want a different name.

If the binding name or the Secret name is already taken, the binding is refused. Set `name` or `secret_name` to choose another. The provider does not retry with a generated name. The instance's limit of 20 bound namespaces counts across all its PostgreSQL databases.

Before you use the readwrite, readonly or custom bindings of an added database, connect through an owner binding and run the grants inside it. `Ready` confirms provisioning and the Secret, but does not check your SQL grants. See [Logical databases](database.md#logical-databases).

### Use the credentials Secret

Inside the cluster, each bound namespace sees a Service named after the database and the Secret named by `secret_name`, with the keys `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`. `type` is `postgresql` and `provider` is `kupe`.

Mount the Secret as a volume at `/bindings/<secret_name>` and set `SERVICE_BINDING_ROOT=/bindings`. `uri` is a `postgresql://` connection string with `sslmode=verify-full` and `sslrootcert=/bindings/<secret_name>/ca.crt`, so it only connects from that mount path. Mounted elsewhere, build the connection from `host`, `port`, `database`, `username`, `password`, `sslmode` and `ca.crt` instead.

The example below uses the [hashicorp/kubernetes](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs) provider, configured for the bound cluster, to mount the Secret into a Deployment in `orders-api`.

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

### Change the namespaces

Changes to `namespaces` apply in place, and Terraform sends the complete new list. An added namespace gets the Service and the Secret. A namespace you take out loses its Secret, and also the Service and network path unless another binding of the database lists it. Namespaces in both lists keep working throughout. Update waits until the platform has delivered the new list, including withdrawing from the namespaces taken out.

A binding can list at most 10 namespaces, which the plan checks. All bindings of one database together can list at most 20, and a namespace can be in only one binding per cluster and role of a database. The API refuses a change that breaks either rule when it is applied; taking namespaces out never does.

### Dedicated credentials and rotation

Set `credentials = { dedicated = true }` to give every listed namespace its own login and password. Turning dedicated credentials on updates the binding in place, even when `namespaces` changes in the same apply. Leaving out `credentials` means `dedicated = false`. Turning a dedicated binding back to `false`, including by removing the `credentials` block, replaces the binding. Owner bindings use shared credentials. Custom bindings need `role_name` and dedicated credentials.

When `logical_database` is left out or matches the instance name with hyphens replaced by underscores, a custom role such as `sales_reports` gives the default binding name `{database}-{cluster}-sales-reports`. Any other explicit selection, including a restored first name that differs, adds `logical_database` to the name as described above. The default Secret name for the first database is `{database}-sales-reports`.

To rotate dedicated credentials, set or change `credentials.rotation_trigger`: each change requests one rotation on update, and removing the trigger does not rotate. The trigger is your own marker, such as a ticket number, and Terraform stores it in state. Passwords never reach the provider.

Update then follows the rotation's request token until the platform has applied it and prepared the credentials Secret for every listed namespace. `observed_rotation` records the completed token and `namespace_status[].delivered_rotation` shows progress. If another rotation supersedes the request, Update follows the newer token. The copy into the cluster happens afterwards and is not observed, so restart applications once it is delivered.

### Pending and failed bindings

A binding is `Pending` while its database or cluster does not exist, is not running yet or is being deleted, and while any namespace it lists has not been delivered. Create and Update wait until their timeout, then fail. The platform keeps trying, and the binding becomes `Ready` once every listed namespace is.

A namespace is `Failed` when it already has a Service or Endpoints object named after the database, or a Secret named `secret_name`, that the platform did not create. The binding does not replace that object, and the other namespaces keep working. Create and Update stop straight away with an error that names the namespace, the database and the Secret name, and `namespace_status` carries the platform's message for it.

- After a failed Create, Terraform keeps the binding in state, marked tainted: rename or remove the object, or set a different `secret_name`, then run `terraform apply` to replace the binding. `terraform destroy` removes it.
- After a failed Update, the new list is in state. Rename or remove the object and the platform delivers into the namespace on its next try, or take the namespace out of `namespaces` and apply.

### Waiting and timeouts

Create and Update wait for the current generation and for every listed namespace to be `Ready`. A binding that fails or times out on create stays in state, marked tainted. Delete waits until the binding is gone. The timeouts default to 5 minutes for create and delete and 10 minutes for update. Set longer ones with `timeouts`, for example `timeouts = { create = "10m" }`.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `cluster` (String) Cluster to bind into. It does not have to exist yet: the binding becomes `Ready` once it does, and until then Create waits up to its timeout and then fails.
- `database` (String) Name of the database to bind.
- `namespaces` (Set of String) Namespaces in the cluster that get the Service and credentials Secret: 1 to 10, and none of them `kube-*` or `argocd`. Changes apply in place.
- `role` (String) Role granted: `owner` (can change the schema), `readwrite` (reads and writes data), `readonly` (reads data), or `custom` (a declared role).

### Optional

- `credentials` (Attributes) Dedicated credentials give every namespace its own login and password. Passwords are never returned to Terraform. (see [below for nested schema](#nestedatt--credentials))
- `logical_database` (String) PostgreSQL database to bind. Leave it out to bind the first database, which for a restored instance is the first name it kept from its source. Changing it replaces the binding. An added database's name is part of the default Secret name; see `name` for the default binding name. If a default name is taken, set `name` or `secret_name`.
- `name` (String) Binding name. Defaults to `{database}-{cluster}-{role}` when `logical_database` is omitted or equals the instance name with hyphens replaced by underscores. Other explicit selections use `{database}-{logical_database}-{cluster}-{role}`, including a restored first database whose name differs from the normalised instance name. Uses `role_name` for custom and replaces underscores with hyphens. If `logical_database` is explicitly unknown during planning, the default name becomes known when the selection is known. Set `name` explicitly if the default is taken, longer than 63 characters, or you need a second binding of the same role into the same cluster.
- `role_name` (String) Declared role granted by a custom binding. Required for custom and refused for built-in roles. Changing it replaces the binding.
- `secret_name` (String) Name of the credentials Secret in every listed namespace. When unset, a new binding uses `{database}-{role}` for the first PostgreSQL database or `{database}-{logical_database}-{role}` for an added database (using `role_name` for custom, with underscores replaced by hyphens). A taken default is refused; set `secret_name` explicitly. An existing or imported binding keeps its current name, so removing the attribute from configuration does not change the name back: set it to `{database}-{role}` instead. A different name replaces the binding. The Secret holds `type`, `provider`, `host`, `port`, `database`, `username`, `password`, `sslmode`, `ca.crt` and `uri`; mount it at `/bindings/<secret_name>`, where `uri` expects `ca.crt`.
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))

### Read-Only

- `etag` (String) Resource version used for optimistic locking during updates.
- `namespace_status` (Attributes List) What the binding delivered into each namespace, sorted by name. A namespace removed from `namespaces` stays listed until its Service and Secret are withdrawn. (see [below for nested schema](#nestedatt--namespace_status))
- `observed_rotation` (String) Dedicated rotation token applied and delivered to every listed namespace; empty for shared credentials.
- `phase` (String) Current phase: `Pending` (waiting for the database, the cluster or a namespace), `Ready` (every listed namespace is `Ready`) or `Failed` (a listed namespace already has a Service, Endpoints object or Secret of the same name that the platform did not create).

<a id="nestedatt--credentials"></a>
### Nested Schema for `credentials`

Optional:

- `dedicated` (Boolean) Give each namespace its own login and password. Leaving it out, or the whole `credentials` block, means `false`. Turning it on is an in-place change, and turning it off, including by removing the block, replaces the binding. Owner bindings use shared credentials, and custom bindings need dedicated credentials.
- `rotation_trigger` (String) Set or change this value to rotate dedicated credentials on update. Removing it does not rotate. It is any value you choose, not a password, and Terraform keeps it in state. Restart applications once the new credentials are delivered.


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
- `host` (String) In-cluster DNS name applications in this namespace connect to. For a `readwrite`, `readonly` or custom binding of a database with the connection pooler on, it is the pooler's, for example `orders-db--pooler.orders-api.svc.cluster.local`: the namespace moves there once the pooler has served and stays there while the pooler is on, and only turning the pooler off moves it back. For an `owner` binding, and before the pooler has served or after it is turned off, it is the database's, for example `orders-db.orders-api.svc.cluster.local`. Empty until the Service is delivered.
- `message` (String) Why the namespace is not Ready; empty when it is.
- `name` (String) Namespace inside the cluster.
- `phase` (String) `Pending`, `Ready` or `Failed` (the namespace already has a Service, Endpoints object or Secret of the same name that the platform did not create).

## Import

Import is supported using the following syntax:

```shell
# <database>/<binding>
terraform import kupe_database_binding.api orders-db/orders-db-prod-readwrite
```
