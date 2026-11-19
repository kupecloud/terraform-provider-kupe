---
page_title: "kupe_secret Resource - kupe"
subcategory: ""
description: |-
  Manages a Kupe Cloud secret: a Vault path whose values Kupe delivers as Kubernetes Secrets to the cluster namespaces you list. Write values with values_wo and values_wo_version, which need OpenTofu 1.11 or Terraform 1.11.1 or later. The values never reach plan or state, and destroying the secret keeps them in Vault. Create and Update wait until every target has the current values and removed targets are withdrawn; the timeouts default to 2 minutes.
---

# kupe_secret (Resource)

Manages a Kupe Cloud secret: a Vault path whose values Kupe delivers as Kubernetes Secrets to the cluster namespaces you list. Write values with `values_wo` and `values_wo_version`, which need OpenTofu 1.11 or Terraform 1.11.1 or later. The values never reach plan or state, and destroying the secret keeps them in Vault. Create and Update wait until every target has the current values and removed targets are withdrawn; the timeouts default to 2 minutes.

## Example usage

```terraform
# Use OpenTofu 1.11 or Terraform 1.11.1 or later.
variable "app_config_values" {
  description = "Every value to store and deliver. Pass them in at plan and apply time, never in a file."
  type        = map(string)
  sensitive   = true
  ephemeral   = true
}

resource "kupe_secret" "app_config" {
  name        = "app-config"
  secret_path = "apps/app-config"

  values_wo         = var.app_config_values
  values_wo_version = 1

  sync = [
    {
      cluster   = "prod"
      namespace = "hello"
      keys      = ["username", "password"]
    },
    {
      cluster   = "staging"
      namespace = "hello"
    },
  ]
}
```

### Write values

Write-only values need OpenTofu 1.11 or later, or Terraform 1.11.1 or later. CI tests OpenTofu 1.11.0 and 1.13.2 and Terraform 1.11.1 and 1.17.1.

Pass `values_wo` through an ephemeral, sensitive variable, as in the example. The values never reach the plan or state, and the provider reads only the entry's version, never its values. Keep the values themselves out of configuration files, variable defaults, outputs and shell history.

Set `values_wo` together with a positive `values_wo_version`. Each time you change the version, the provider replaces the whole stored map, so keys you leave out are removed from the entry. Changing `values_wo` without changing the version sends nothing. Removing both sends nothing and keeps the stored values.

On create, there must be no entry at the path yet, including one marked as deleted. To manage an existing entry, apply the resource once without the two inputs, then add them. Import reads `source_version` from the entry and leaves `values_wo_version` null.

An update checks that the entry is still at the `source_version` shown in the plan. A conflict means another client changed it after the plan read it: run plan again and review the new version before you apply. If the provider cannot tell whether a write was saved, it keeps the previous `values_wo_version`, and the next apply after a fresh plan sends the values again. The provider never retries a value write on its own.

After a confirmed write, the provider records the new `values_wo_version` and `source_version` before it waits for delivery. If an update then times out on delivery, the error says “values saved as version V; delivery is still in progress”, and the next plan does not send the values again. Check delivery in the Kupe console, and change the version only when you want to replace the values again.

Destroy removes the definition and withdraws its delivered copies. The stored values stay in Vault, and Terraform cannot delete them: use `kupe secret purge --path PATH`.

### Select keys

Set `keys` on a target to deliver only those keys from the stored entry. Without `keys`, the target gets the whole entry. An empty set is refused. Each target can select 1 to 64 keys, and all targets together at most 1,024.

Terraform sends the complete target list, keys included, with every change. Adding or removing a key updates the definition in place. Removing `keys` from a target delivers the whole entry to it.

### Delivery and timeouts

`sync_status` lists each target's phase and reason, sorted by cluster then namespace. `Synced` means the current values have reached that namespace. `Active` on its own does not confirm that a change was delivered, so Create and Update also wait for the current generation and for every target to be `Synced`.

A removed target stays `Withdrawing` until the platform has removed its delivery objects. The copy inside the cluster goes shortly after, or later if the cluster is down. Create and Update wait for withdrawal to finish.

A current target in `TargetConflict` ends the wait with an error that names the cluster and namespace; choose a different `secret_name` for that target. For other delivery failures, including `ProviderError` for a missing entry or selected key, the provider keeps polling until the timeout. A definition that is being deleted also ends the wait straight away.

After a confirmed value write and an accepted Create, a delivery timeout is a warning. The definition and the confirmed versions stay in state, and the next plan does not send the values again. A refused write, an uncertain write, a definition failure or a terminal delivery failure is still an error. When a Create without a value write fails or times out on delivery, the definition stays in state marked tainted, and the next apply replaces it.

When the API accepts an Update and delivery then fails or times out, the change stays in state and the apply returns an error. To send the values again, change `values_wo_version`. To undo a target change, restore the previous configuration and apply.

Delete waits for the definition to disappear. If withdrawal takes longer than the delete timeout, Terraform removes the definition from state with a warning. The platform finishes in the background and keeps the name reserved until it is done. The stored values are kept.

The create, update and delete timeouts default to 2 minutes. Set longer ones with `timeouts`, for example `timeouts = { create = "5m" }`.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `name` (String) Secret name. Changing it replaces the secret.
- `secret_path` (String) Vault path of the stored values. Changing it replaces the secret.

### Optional

> **NOTE**: [Write-only arguments](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments) are supported in Terraform 1.11 and later.

- `sync` (Attributes List) Cluster namespaces to deliver the values to. Changes apply in place. All targets together can select at most 1,024 keys. (see [below for nested schema](#nestedatt--sync))
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))
- `values_wo` (Map of String, Sensitive, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) Complete set of values to store, replacing what is there. Write-only and sensitive, so never stored in plan or state. Set it with `values_wo_version` and change that number for each write. Removing both keeps the stored values.
- `values_wo_version` (Number) Positive number that controls when `values_wo` is sent. Change it to replace the stored values. While it stays the same, nothing is sent, even if `values_wo` changes.

### Read-Only

- `created_at` (String) Timestamp when the managed secret definition was created.
- `etag` (String) Resource version used for optimistic locking during updates.
- `phase` (String) `Pending`, `Active`, `Degraded` or `Deleting`. `Active` on its own does not confirm that the current values were delivered; see `sync_status`.
- `source_version` (Number) Version of the Vault entry, or null when there is no entry. A value write checks the version shown in the plan and refuses to overwrite a change made since.
- `sync_status` (Attributes List) Delivery status of each target, sorted by cluster then namespace. A removed target stays `Withdrawing` until the platform has withdrawn it. (see [below for nested schema](#nestedatt--sync_status))

<a id="nestedatt--sync"></a>
### Nested Schema for `sync`

Required:

- `cluster` (String) Target cluster name.
- `namespace` (String) Target namespace in the cluster.

Optional:

- `keys` (Set of String) Keys to deliver to this target, 1 to 64. Each key is 1 to 253 ASCII letters, digits, `-`, `_` or `.`, and cannot be `.` or start with `..`. Omit it to deliver the whole entry.
- `secret_name` (String) Name of the Kubernetes Secret in the namespace. Defaults to the secret's `name`.


<a id="nestedatt--timeouts"></a>
### Nested Schema for `timeouts`

Optional:

- `create` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).
- `delete` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours). Setting a timeout for a Delete operation is only applicable if changes are saved into state before the destroy operation occurs.
- `update` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).


<a id="nestedatt--sync_status"></a>
### Nested Schema for `sync_status`

Read-Only:

- `cluster` (String) Target cluster name.
- `namespace` (String) Target namespace in the cluster.
- `phase` (String) `Pending`, `Synced`, `Failed` or `Withdrawing`. `Synced` means the current values have reached the namespace.
- `reason` (String) Why the target is in its phase, such as `TargetConflict`, `ProviderError` or `AwaitingCopy`.
- `secret_name` (String) Name of the Kubernetes Secret in the namespace.

## Import

Import is supported using the following syntax:

```shell
terraform import kupe_secret.app_config app-config
```
