# Terraform Provider for Kupe Cloud

The Kupe provider lets you manage tenant-scoped Kupe Cloud resources with Terraform or OpenTofu.

Use it when you want reviewable changes, repeatable tenant setup, and a clean way to manage clusters, secrets, members, and API keys alongside the rest of your infrastructure code.

## What the provider manages

- managed clusters with `kupe_cluster`
- tenant secrets and sync targets with `kupe_secret`
- tenant membership with `kupe_tenant_member`
- machine-to-machine credentials with `kupe_api_key`
- tenant, cluster, and plan metadata through data sources

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

Use `KUPE_API_KEY` for authentication in hosted Kupe Cloud environments.

For normal environments, use an `https://` API host. Plain HTTP is only supported for local development endpoints such as `http://localhost:8080`.

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

- `make build` builds the provider binary
- `make test` runs unit tests
- `make vet` runs `go vet`
- `make tofu-validate` validates the local provider against the example configurations
- `make docs` installs `tfplugindocs`, generates registry docs, and validates them

## Registry docs

Terraform and OpenTofu registry docs are generated from this repo with `tfplugindocs` and written to `docs/`.

That generated docs tree is the source of truth for provider reference documentation. Kupe-specific internal notes and development docs live in `docs-internal`, not in this repo.

## Repo layout

- `internal/provider/` contains the Terraform provider, resources, and data sources
- `internal/client/` contains the Kupe API client
- `examples/` contains example resource and data source usage
- `scripts/` contains local validation and docs generation helpers

## Notes

- API keys are stored in Terraform state. Use an encrypted remote backend and restrict access to state.
- Generated docs should be refreshed with `make docs` whenever provider schemas or examples change.
