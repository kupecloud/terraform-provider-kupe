---
page_title: "kupe_app_environment Resource - kupe"
subcategory: ""
description: |-
  Manages one Kupe app environment. Applying deploying settings requests a release. A failed first deployment remains in state without taint; a failed update records the accepted settings and reports an error. Increase deployment_revision by one to retry. Destroying the environment deletes its namespace and everything in it; the cluster is kept. Turn deletion protection off and apply before destroying. Do not independently target this namespace with a kupe_secret or binding.
---

# kupe_app_environment (Resource)

Manages one Kupe app environment. Applying deploying settings requests a release. A failed first deployment remains in state without taint; a failed update records the accepted settings and reports an error. Increase deployment_revision by one to retry. Destroying the environment deletes its namespace and everything in it; the cluster is kept. Turn deletion protection off and apply before destroying. Do not independently target this namespace with a kupe_secret or binding.

## Example usage

```terraform
data "kupe_app_image" "web" {
  image = "ghcr.io/kupecloud/hello-kupe:1.5.1"
}

resource "kupe_app" "this" {
  name = "shop"
}

resource "kupe_app_environment" "this" {
  app     = kupe_app.this.name
  name    = "production"
  cluster = "europe"

  web = {
    image = data.kupe_app_image.web.reference
    port  = 8080
    env = [{ name = "LOG_LEVEL", value = "info" }]
    health_checks = { readiness = { http = { path = "/healthz" } } }
  }

  # Write values at this path in the console Vault page before referencing
  # them in web.env. Do not target this environment's namespace separately.
  # secrets = [{ name = "payments", path = "apps/shop/production/payments" }]

  # Increase by one to retry an unchanged deployment.
  # deployment_revision = 2

  # Before destroy, set this to false and apply.
  # deletion_protection = false
}
```

### Multiple environments

```terraform
resource "kupe_app" "multi" {
  name = "shop-multi"

  # Before destroy, set this to false and apply.
  # deletion_protection = false
}

locals {
  app_environments = {
    staging    = { cluster = "europe", replicas = 1 }
    production = { cluster = "europe", replicas = 2 }
  }
}

resource "kupe_app_environment" "multi" {
  for_each = local.app_environments
  app      = kupe_app.multi.name
  name     = each.key
  cluster  = each.value.cluster

  web = {
    image    = "ghcr.io/kupecloud/hello-kupe@sha256:be393b04d81f3a3e63d57be12dce11e823b1af75ea3997eecbc2d19a3b434f93"
    port     = 8080
    replicas = each.value.replicas
  }

  # Before destroy, set this to false and apply.
  # deletion_protection = false
}
```

## Image tags and digests

Use `data.kupe_app_image` to resolve a tag or digest, then set `web.image` to its `reference`. The environment accepts only a sha256 digest reference. The data source calls the Kupe API on each read and the provider keeps no image cache.

Pin a version tag in production. If a moving tag such as `latest` points to a new digest, the next plan shows a `web.image` change and applying that plan deploys it. A version tag can also be reassigned by its publisher; use a digest input when the image identity must stay fixed.

## Deploying and retrying

An apply that changes `web`, a referenced secret path or `deployment_revision` requests a deployment and waits for its requested release. Domain and protection changes wait for the observed settings without starting a release. The default timeouts are create 30 minutes, update 20 minutes and delete 20 minutes.

A failed first deploy, or one that times out while deploying, gives a warning. Terraform retains the resource without taint, so an unchanged apply plans no change. A failed update reports an error and retains the accepted configuration; an unchanged apply does not resubmit it.

Increase `deployment_revision` by exactly one to retry. For example, after revision 1, set it to 2 and apply. A retry can accompany settings changes: the provider sends the settings first, then retries using the returned ETag. A failed requested release remains failed when recovery succeeds; the diagnostic names the release now serving.

If another client changes the environment before its release can be identified, Create warns and Update errors: “the environment changed before its release could be identified; check its current state before deploying again”. This can also happen when the concurrent edit only changes domains or protection. The provider retains the accepted settings and sends no further deployment request during that apply. A later refresh can show another client’s settings; review the next plan before applying again. This outcome does not assert that the release was Superseded.

## Vault references

Each `secrets` entry points to a tenant Vault path. Write its values in the console Vault page. Environment variables reference its declared name and key. Secret values are never provider attributes.

Do not also target the environment's namespace with an independent `kupe_secret` or service binding. Remove independent deliveries before deleting the environment. The API refuses removal of a secret entry while `web.env` still references it.

## Deletion

Destroying an environment deletes its namespace and everything in it. The cluster and Vault entries are kept. Set `deletion_protection = false` and apply first, then destroy it. Delete waits until the environment is gone; if cleanup exceeds the delete timeout, Terraform removes it from state with a warning while the platform continues cleanup.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `app` (String) App identity. Reference `kupe_app.this.name` to order creation and deletion.
- `cluster` (String) Cluster that runs the environment. Changing it replaces the environment.
- `name` (String) Environment name: an RFC 1123 label of at most 20 characters without `--`.
- `web` (Attributes) Complete runtime configuration. Images must be pinned by digest. (see [below for nested schema](#nestedatt--web))

### Optional

- `deletion_protection` (Boolean) Set to false and apply before destroying the environment.
- `deployment_revision` (Number) Increase by one to retry the current deployment. An unchanged apply never retries.
- `domains` (Set of String) Complete set of exact lowercase custom hostnames. Domain changes do not deploy.
- `secrets` (Attributes List) Vault paths referenced by the web workload. Write their values in the console Vault page. (see [below for nested schema](#nestedatt--secrets))
- `timeouts` (Attributes) (see [below for nested schema](#nestedatt--timeouts))

### Read-Only

- `active_release` (String) Release currently serving.
- `address` (String) Generated HTTPS address.
- `deployment_phase` (String) Outcome of the requested release, independently of recovery.
- `deployment_reason` (String) Reason for the requested release's outcome.
- `etag` (String) Resource version used for optimistic locking during updates.
- `last_successful_release` (String) Most recent successful release.
- `namespace` (String) Namespace inside the cluster. Destroying the environment deletes this namespace and everything in it.
- `phase` (String) Current environment phase.
- `recovery_phase` (String) Recovery outcome after a failed request.
- `recovery_release` (String) Release used for recovery.
- `requested_release` (String) Requested release name.

<a id="nestedatt--web"></a>
### Nested Schema for `web`

Required:

- `image` (String) Image including a registry host and sha256 digest.
- `port` (Number) Container port.

Optional:

- `args` (List of String) Arguments, at most 32 strings of 1024 bytes each.
- `command` (List of String) Entrypoint, at most 32 strings of 1024 bytes each.
- `env` (Attributes List) Ordered environment variables. Choose value or secret_key_ref per entry. (see [below for nested schema](#nestedatt--web--env))
- `health_checks` (Attributes) Readiness and optional liveness probes. (see [below for nested schema](#nestedatt--web--health_checks))
- `replicas` (Number) Replica count, 1 to 10.
- `resources` (Attributes) Requests and limits. (see [below for nested schema](#nestedatt--web--resources))

<a id="nestedatt--web--env"></a>
### Nested Schema for `web.env`

Required:

- `name` (String) Environment variable name.

Optional:

- `secret_key_ref` (Attributes) Reference to a declared secret, never a secret value. (see [below for nested schema](#nestedatt--web--env--secret_key_ref))
- `value` (String) Literal value. Use secret_key_ref for credentials.

<a id="nestedatt--web--env--secret_key_ref"></a>
### Nested Schema for `web.env.secret_key_ref`

Required:

- `key` (String) Key in the Vault entry.
- `secret` (String) Declared secret name.



<a id="nestedatt--web--health_checks"></a>
### Nested Schema for `web.health_checks`

Optional:

- `liveness` (Attributes) Choose exactly one of tcp or http. (see [below for nested schema](#nestedatt--web--health_checks--liveness))
- `readiness` (Attributes) Choose exactly one of tcp or http. (see [below for nested schema](#nestedatt--web--health_checks--readiness))

<a id="nestedatt--web--health_checks--liveness"></a>
### Nested Schema for `web.health_checks.liveness`

Optional:

- `http` (Attributes) HTTP probe on the web port. (see [below for nested schema](#nestedatt--web--health_checks--liveness--http))
- `tcp` (Attributes) TCP probe on the web port. (see [below for nested schema](#nestedatt--web--health_checks--liveness--tcp))

<a id="nestedatt--web--health_checks--liveness--http"></a>
### Nested Schema for `web.health_checks.liveness.http`

Required:

- `path` (String) HTTP path beginning with /.


<a id="nestedatt--web--health_checks--liveness--tcp"></a>
### Nested Schema for `web.health_checks.liveness.tcp`



<a id="nestedatt--web--health_checks--readiness"></a>
### Nested Schema for `web.health_checks.readiness`

Optional:

- `http` (Attributes) HTTP probe on the web port. (see [below for nested schema](#nestedatt--web--health_checks--readiness--http))
- `tcp` (Attributes) TCP probe on the web port. (see [below for nested schema](#nestedatt--web--health_checks--readiness--tcp))

<a id="nestedatt--web--health_checks--readiness--http"></a>
### Nested Schema for `web.health_checks.readiness.http`

Required:

- `path` (String) HTTP path beginning with /.


<a id="nestedatt--web--health_checks--readiness--tcp"></a>
### Nested Schema for `web.health_checks.readiness.tcp`




<a id="nestedatt--web--resources"></a>
### Nested Schema for `web.resources`

Optional:

- `limits` (Attributes) (see [below for nested schema](#nestedatt--web--resources--limits))
- `requests` (Attributes) (see [below for nested schema](#nestedatt--web--resources--requests))

<a id="nestedatt--web--resources--limits"></a>
### Nested Schema for `web.resources.limits`

Optional:

- `cpu` (String) CPU quantity.
- `memory` (String) Memory quantity.


<a id="nestedatt--web--resources--requests"></a>
### Nested Schema for `web.resources.requests`

Optional:

- `cpu` (String) CPU quantity.
- `memory` (String) Memory quantity.




<a id="nestedatt--secrets"></a>
### Nested Schema for `secrets`

Required:

- `name` (String) Name used by secret_key_ref.
- `path` (String) Tenant Vault path.


<a id="nestedatt--timeouts"></a>
### Nested Schema for `timeouts`

Optional:

- `create` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).
- `delete` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours). Setting a timeout for a Delete operation is only applicable if changes are saved into state before the destroy operation occurs.
- `update` (String) A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration) consisting of numbers and unit suffixes, such as "30s" or "2h45m". Valid time units are "s" (seconds), "m" (minutes), "h" (hours).

## Import

```shell
terraform import kupe_app_environment.this shop/production
```
