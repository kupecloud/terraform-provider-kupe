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
* [Local testing](#local-testing)
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
* `make test` runs unit tests
* `make vet` runs `go vet`
* `make tofu-validate` validates the local provider against the example
  configurations
* `make docs` installs `tfplugindocs`, generates registry docs, and
  validates them

## Local testing

Terraform and OpenTofu providers are plugin binaries. For local CLI
testing, you do need a compiled binary. There is no source-only mode
where Terraform or OpenTofu runs the provider directly from Go files.

You do not need to run `make build` before `make tofu-validate`. That
validation flow already builds a temporary local binary and wires it in
through a dev override automatically.

Use the following commands when you want to test the provider against a
local Kupe API:

```bash
make local-provider
export TF_CLI_CONFIG_FILE="$PWD/.tmp/tfdevrc"
```

Then use a scratch Terraform or OpenTofu configuration that points at
your local API, for example:

```hcl
terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}

provider "kupe" {
  host   = "http://localhost:8080"
  tenant = "example-tenant"
  # api_key is read from KUPE_API_KEY
}
```

```bash
export KUPE_API_KEY="kupe_..."
tofu init
tofu plan
```

Use `make build` when you want the standalone binary in the repo root.
Use `make local-provider` when you want Terraform or OpenTofu to run the
provider locally through a dev override.

## Registry docs

Terraform and OpenTofu registry docs are generated from this repo with
`tfplugindocs` and written to `docs/`.

That generated docs tree is the source of truth for provider reference documentation.

## Release workflow

The main CI flow is defined in `.github/workflows/main.yaml`.

* lint, unit test, security, and build jobs run on every push to `main`
* the semantic release step uses the reusable workflow from
  `kupecloud/github-workflows` pinned to a commit SHA
* a separate local `publish` workflow exists in
  `.github/workflows/publish.yaml`

The `publish` job in `main.yaml` is intentionally commented out right
now. That means the repo can cut a semantic release version, but it does
not yet automatically attach signed Terraform and OpenTofu registry
artifacts to the GitHub release.

When you are ready to publish registry artifacts from CI, enable the
commented `publish` job in `.github/workflows/main.yaml` and make sure
the required `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` secrets are
configured. The publish workflow builds both registry variants with
GoReleaser and uploads the signed artifacts to the matching GitHub
release tag.

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
