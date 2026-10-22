---
page_title: "Kupe Provider"
description: |-
  Use the Kupe provider to manage tenant-scoped Kupe Cloud resources with Terraform or OpenTofu.
---

# Kupe Provider

The Kupe provider manages tenant-scoped Kupe Cloud resources including clusters, databases, the container registry, secrets, members, API keys, and per-tenant Alertmanager configuration.

Use it when you want:

- reviewed infrastructure changes through plan and apply
- repeatable tenant setup in Terraform or OpenTofu
- platform resources managed alongside the rest of your infrastructure code

## Example Usage

```terraform
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

## State contains secrets

`kupe_api_key.key` and `kupe_registry_token.secret` are stored in Terraform state in plain text. Marking them Sensitive hides them from plan and apply output but does not keep them out of the state file. Keep state in an encrypted backend with restricted access, or create API keys and access tokens outside Terraform.

## Available Resources

- `kupe_cluster`
- `kupe_database`
- `kupe_database_binding`
- `kupe_registry`
- `kupe_registry_binding`
- `kupe_registry_token`
- `kupe_secret`
- `kupe_tenant_member`
- `kupe_api_key`
- `kupe_alertmanager_global`
- `kupe_alertmanager_receiver`
- `kupe_alertmanager_routes`

## Available Data Sources

- `kupe_tenant`
- `kupe_cluster`
- `kupe_database`
- `kupe_registry`
- `kupe_plan`
