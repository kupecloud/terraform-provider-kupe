# Terraform Provider for Kupe Cloud

The Kupe provider lets you manage tenant-scoped Kupe Cloud resources
with Terraform or OpenTofu.

Use it when you want reviewable changes, repeatable tenant setup, and a
clean way to manage clusters, secrets, members, and API keys alongside
the rest of your infrastructure code.

<!-- toc -->

* [What the provider manages](#what-the-provider-manages)
* [Quick start](#quick-start)
* [Example resource](#example-resource)
* [Development](#development)
* [Running tests](#running-tests)
* [Testing the provider against a live API](#testing-the-provider-against-a-live-api)
  * [Smoke testing against a real tenant](#smoke-testing-against-a-real-tenant)
  * [Testing against another tenant or a local API](#testing-against-another-tenant-or-a-local-api)
* [Registry docs](#registry-docs)
* [Release workflow](#release-workflow)
* [Repo layout](#repo-layout)
* [Notes](#notes)

<!-- Regenerate with "pre-commit run -a markdown-toc" -->

<!-- tocstop -->

## What the provider manages

* managed clusters with `kupe_cluster`
* tenant secrets and sync targets with `kupe_secret`
* tenant membership with `kupe_tenant_member`
* machine-to-machine credentials with `kupe_api_key`
* tenant, cluster, and plan metadata through data sources

## Quick start

```hcl
terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}

provider "kupe" {
  host   = "https://api.kupe.cloud"
  tenant = "example-tenant"
  # api_key is read from KUPE_API_KEY
}
```

Use `KUPE_API_KEY` for authentication in hosted Kupe Cloud
environments.

For normal environments, use an `https://` API host. Plain HTTP is only
supported for local development endpoints such as
`http://localhost:8080`.

## Example resource

```hcl
resource "kupe_cluster" "production" {
  name         = "production"
  display_name = "Production"
  type         = "shared"
  version      = "1.31"

  resources = {
    cpu     = "4"
    memory  = "16Gi"
    storage = "100Gi"
  }
}
```

## Development

Common commands:

```bash
make test
make vet
make tofu-validate
make docs
```

Useful targets:

* `make build` builds the provider binary
* `make local-provider` builds a local provider binary and writes a dev
  override config under `.tmp/`
* `make test` runs unit and acceptance tests against the in-process mock
  API (see [Running tests](#running-tests))
* `make vet` runs `go vet`
* `make tofu-validate` validates the local provider against the example
  configurations
* `make docs` installs `tfplugindocs`, generates registry docs, and
  validates them

## Running tests

`make test` runs both the plain unit tests under `internal/client/` and
the acceptance tests under `internal/provider/`. The acceptance tests
use `terraform-plugin-testing`, which spins up the provider in-process
and drives it through a real Terraform or OpenTofu CLI against a stateful
mock Kupe API defined in `internal/provider/testutil_test.go`. No live
Kupe API or external network access is needed.

A local CLI is required. `make test` auto-detects one in this order:

1. `tofu` from your `PATH`
2. `terraform` from your `PATH`

Install OpenTofu (recommended) or Terraform first if neither is on your
`PATH`. On macOS:

```bash
brew install opentofu
```

Then:

```bash
make test
```

If the detected binary is `tofu`, the Makefile sets
`TF_ACC_PROVIDER_HOST=registry.opentofu.org` so the framework uses the
OpenTofu namespace instead of the legacy `-` namespace, which OpenTofu
rejects. You can override the auto-detected values:

```bash
make test TF_ACC_TERRAFORM_PATH=/usr/local/bin/terraform
```

To run a single test:

```bash
TF_ACC_TERRAFORM_PATH="$(command -v tofu)" \
TF_ACC_PROVIDER_HOST=registry.opentofu.org \
TF_ACC_PROVIDER_NAMESPACE=kupecloud \
  go test -run TestAccClusterResource -v ./internal/provider/...
```

> Setting `TF_ACC_TERRAFORM_PATH` is what tells the framework to use the
> locally installed CLI. Without it, `terraform-plugin-testing` falls
> back to downloading Terraform via `hc-install` and verifying its GPG
> signature — that path breaks when the embedded HashiCorp key expires.

## Testing the provider against a live API

Terraform and OpenTofu providers are plugin binaries — there is no
source-only mode where Terraform/OpenTofu runs the provider directly
from Go files. The local flow is:

1. Build the provider with `make local-provider`. This compiles the
   provider into `.tmp/plugins/...` and writes a dev-override CLI config
   at `.tmp/tfdevrc`.
2. Point your shell at that CLI config so Terraform/OpenTofu uses your
   local binary instead of the registry:

   ```bash
   export TF_CLI_CONFIG_FILE="$PWD/.tmp/tfdevrc"
   ```

3. Export an API key for the tenant you're testing against:

   ```bash
   export KUPE_API_KEY=kupe_...
   ```

4. Run `tofu plan` / `tofu apply` against a workspace that points at the
   Kupe API. **Skip `tofu init`** — with a dev override active, tofu
   uses your local binary directly and `init` only adds a doomed
   registry lookup.

You do **not** need `make build` first. `make local-provider` builds the
binary itself, and `make tofu-validate` builds its own temporary binary.

### Smoke testing against a real tenant

The repo ships a manual smoke workspace at [`test/manual/`](test/manual).
Each `<resource_type>.tf` defines a single resource labelled `smoke`, so
you can apply them one at a time. The workspace's `provider "kupe"`
block is intentionally empty — host, tenant, and API key all come from
environment variables, so no environment-specific URL or tenant name is
committed to this public repo:

```bash
# from the repo root
make local-provider
export TF_CLI_CONFIG_FILE="$PWD/.tmp/tfdevrc"

export KUPE_HOST=https://api.<your-env>.kupe.cloud
export KUPE_TENANT=<your-test-tenant>
export KUPE_API_KEY=kupe_...

cd test/manual
# No `tofu init` — the dev override loads your local binary directly.
tofu apply -target=kupe_cluster.smoke -auto-approve
# ... exercise reads / updates ...
tofu destroy -auto-approve
```

Requirements:

* Network access to the chosen API. Internal Kupe dev environments
  require a WireGuard tunnel.
* A test tenant exists on the target cluster (see the header comment in
  `test/manual/provider.tf` and `docs/testing.md` for fixture details).

This is the same workspace used as the pre-release smoke step.

### Testing against another tenant or a local API

For a different tenant or a local `kupe-api`, write a scratch workspace
(do not commit it) and point the provider at it:

```hcl
terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}

# host / tenant / api_key all read from KUPE_HOST, KUPE_TENANT,
# KUPE_API_KEY when omitted here.
provider "kupe" {}
```

Then `tofu plan` from that directory with `TF_CLI_CONFIG_FILE` and
`KUPE_API_KEY` exported as above. Skip `tofu init` when the dev override
is active.

`make build` produces a standalone binary in the repo root and is only
useful for `make install` or for shipping a binary out of band. For
local dev iteration, `make local-provider` is what you want.

## Registry docs

Terraform and OpenTofu registry docs are generated from this repo with
`tfplugindocs` and written to `docs/`.

That generated docs tree is the source of truth for provider reference documentation.

## Release workflow

The main CI flow is defined in `.github/workflows/main.yaml`.

* lint, unit test, security, and build jobs run on every push to `main`
* the semantic release step uses the reusable workflow from
  `kupecloud/github-workflows` pinned to a commit SHA
* a `publish` job (currently commented out) builds and signs both the
  Terraform and OpenTofu registry artifacts and attaches them to the
  GitHub release for the cut tag

See [PUBLISHING.md](PUBLISHING.md) for the dual-registry model, the
one-time GPG/secrets setup, registry submission steps for both
`registry.terraform.io` and `registry.opentofu.org`, and the local
dry-run procedure.

## Repo layout

* `internal/provider/` contains the Terraform provider, resources, and data
  sources
* `internal/client/` contains the Kupe API client
* `examples/` contains example resource and data source usage
* `scripts/` contains local validation and docs generation helpers

## Notes

* API keys are stored in Terraform state. Use an encrypted remote
  backend and restrict access to state.
* Generated docs should be refreshed with `make docs` whenever provider
  schemas or examples change.
* The reusable workflows under `.github/workflows/` are pinned to commit
  SHAs rather than floating branch names.
