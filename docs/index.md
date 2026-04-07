---
page_title: "Kupe Provider"
description: |-
  Use the Kupe provider to manage tenant-scoped Kupe Cloud resources with Terraform or OpenTofu.
---

# Kupe Provider

The Kupe provider manages tenant-scoped Kupe Cloud resources including clusters, secrets, members, and API keys.

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

## Available Resources

- `kupe_cluster`
- `kupe_secret`
- `kupe_tenant_member`
- `kupe_api_key`

## Available Data Sources

- `kupe_tenant`
- `kupe_cluster`
- `kupe_plan`
