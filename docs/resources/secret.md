---
page_title: "kupe_secret Resource - kupe"
subcategory: ""
description: |-
  Manages a Kupe Cloud secret definition and its sync targets. Write-only values require OpenTofu 1.11 or Terraform 1.11.1 or later. Set values_wo and bump values_wo_version to replace the stored values; changing values_wo alone sends nothing. Values are never stored in plan or state. A version conflict means another client changed the entry since the plan. Destroy keeps stored values. Create and Update wait until every target has the current values and the platform has withdrawn removed targets. The copy inside the cluster goes shortly after withdrawal, or later if the cluster is down. A target name conflict ends the wait at once. For other delivery failures, the provider keeps polling until the timeout. After a confirmed value write and accepted Create, a delivery timeout returns a warning and keeps the definition and confirmed versions in state. Other Create delivery failures keep the definition marked tainted. Delete waits until the platform has withdrawn the targets and removed the definition; stored values are kept. Set timeouts with the timeouts attribute, for example timeouts = { create = "5m" } (defaults: create/update/delete 2m).
---

# kupe_secret (Resource)

Manages a Kupe Cloud secret definition and its sync targets. Write-only values require OpenTofu 1.11 or Terraform 1.11.1 or later. Set values_wo and bump values_wo_version to replace the stored values; changing values_wo alone sends nothing. Values are never stored in plan or state. A version conflict means another client changed the entry since the plan. Destroy keeps stored values. Create and Update wait until every target has the current values and the platform has withdrawn removed targets. The copy inside the cluster goes shortly after withdrawal, or later if the cluster is down. A target name conflict ends the wait at once. For other delivery failures, the provider keeps polling until the timeout. After a confirmed value write and accepted Create, a delivery timeout returns a warning and keeps the definition and confirmed versions in state. Other Create delivery failures keep the definition marked tainted. Delete waits until the platform has withdrawn the targets and removed the definition; stored values are kept. Set timeouts with the `timeouts` attribute, for example `timeouts = { create = "5m" }` (defaults: create/update/delete 2m).

## Example usage

```terraform
# Use OpenTofu 1.11 or Terraform 1.11.1 or later.
variable "app_config_values" {
  description = "Complete values to store and deliver. Supply them securely when planning and applying."
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

### Writing values

Write-only values require OpenTofu 1.11 or Terraform 1.11.1 or later. CI checks OpenTofu 1.11.0 and 1.13.2, and Terraform 1.11.1 and 1.17.1.

Supply `values_wo` through an ephemeral, sensitive variable, as in the example. Values are never stored in the plan or state. The provider reads only entry metadata. Keep actual values out of configuration files, variable defaults, outputs and command-line history.

Set `values_wo` together with a positive `values_wo_version`. Bump the counter to replace the complete stored map. Omitted keys are removed from the entry. Changing `values_wo` without changing the counter sends nothing. Removing both inputs sends nothing and keeps stored values.

On create, the entry must be absent, including any soft-deleted entry. To manage an existing entry, apply the definition once without the two inputs, then add them. Import reads `source_version` from metadata and leaves `values_wo_version` null.

An update checks the `source_version` shown by the plan. A conflict means another client changed the entry after that version was read. Run plan again to review the current version before applying. A write whose result is uncertain keeps the previous counter; the next apply sends the configured values again after a fresh plan. The provider never automatically retries a value write.

After a confirmed write, the provider records the new counter and source version before waiting for delivery. An update delivery timeout reports “values saved as version V; delivery is still in progress”. The next plan does not resend the values. Check delivery in the Console; bump the counter only when you intend another replacement.

Destroy removes the definition and withdraws its delivered copies; it keeps all stored entry values and versions. The provider does not offer permanent purge.

### Selecting keys

Set `keys` on a target to deliver only those keys from the stored entry. Omit `keys` to deliver the whole entry. An empty set is refused. Each target can select 1 to 64 keys, with at most 1,024 selected keys across all targets.

Terraform sends the complete desired target list, including each target's keys. Adding or removing a key updates the definition in place. Removing `keys` from the configuration delivers the whole entry on that target.

### Waiting for delivery

`sync_status` lists each target's phase and reason, sorted by cluster then namespace. `Synced` means the current values reached that namespace. `Active` alone does not confirm delivery of a change: Create and Update also wait for the current generation and every target to be `Synced`.

A removed target remains `Withdrawing` until the platform has removed its delivery objects. The copy inside the cluster goes shortly after, or later if the cluster is down. Create and Update wait for withdrawal to finish.

A current target with `TargetConflict` ends the wait with an error naming the cluster and namespace. Choose a different `secret_name` for that target. For other delivery failures, including `ProviderError` for a missing entry or selected key, the provider keeps polling until the timeout. A definition being deleted also ends the wait at once.

After a confirmed value write and an accepted Create, a delivery timeout returns a warning. The definition and confirmed versions remain in state, and the next plan does not send the values again. A write refusal, an uncertain write, a definition failure or a terminal delivery failure remains an error. For a Create without a value write, a delivery failure or timeout keeps the definition in state marked tainted; the next apply plans to replace it.

When the API accepts an Update and delivery then fails or times out, the change stays in state and the apply returns an error. To replace the saved values again, bump `values_wo_version`; to back out target settings, restore the previous configuration and apply.

Delete waits for the definition to disappear. If withdrawal exceeds the delete timeout, Terraform removes the definition from state with a warning; the platform finishes in the background and keeps the name reserved until then. Deleting the definition keeps the stored values.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `name` (String) Secret name (immutable after creation).
- `secret_path` (String) Vault path of the stored values. Immutable after creation.

### Optional

> **NOTE**: [Write-only arguments](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments) are supported in Terraform 1.11 and later.

- `sync` (Attributes List) Cluster/namespace targets to sync this secret to. At most 1,024 selected keys across all targets. Changes in place. (see [below for nested schema](#nestedatt--sync))
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))
- `values_wo` (Map of String, Sensitive, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) Complete replacement of the stored values. Write-only and sensitive: never stored in plan or state. Set together with values_wo_version; bump that version for each write. Removing both inputs keeps stored values.
- `values_wo_version` (Number) Positive change counter for values_wo. Bump it to send a complete replacement. An unchanged counter never sends values, even if values_wo changes.

### Read-Only

- `created_at` (String) Timestamp when the managed secret definition was created.
- `etag` (String) Resource version used for optimistic locking during updates.
- `phase` (String) Current phase: Pending, Active, Degraded or Deleting. Active alone does not confirm current delivery.
- `source_version` (Number) Current version of the Vault entry, read from its metadata, or null when there is no entry. A value write checks the version shown by the plan and refuses to overwrite a concurrent change.
- `sync_status` (Attributes List) Per-target delivery status, sorted by cluster then namespace. Removed targets remain Withdrawing until the platform has withdrawn them. (see [below for nested schema](#nestedatt--sync_status))

<a id="nestedatt--sync"></a>
### Nested Schema for `sync`

Required:

- `cluster` (String) Target cluster name.
- `namespace` (String) Target namespace in the cluster.

Optional:

- `keys` (Set of String) Keys to deliver: 1 to 64 per target. Each key is 1 to 253 ASCII letters, digits, '-', '_' or '.', excluding '.', '..' and names starting with '..'. Omit to deliver the whole entry. Changes in place.
- `secret_name` (String) Override the Kubernetes Secret name (defaults to the managed secret name).


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
- `phase` (String) Pending, Synced, Failed or Withdrawing. Synced means the current values reached the namespace.
- `reason` (String) Delivery reason, such as TargetConflict, ProviderError or AwaitingCopy.
- `secret_name` (String) Effective Secret name in the target namespace.

## Import

Import is supported using the following syntax:

```shell
terraform import kupe_secret.app_config app-config
```
