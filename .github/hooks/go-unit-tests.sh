#!/usr/bin/env bash
# Unit and all mock-backed acceptance tests, including write-only saved plans.
# The matrix supplies a CLI; local runs prefer OpenTofu, then Terraform.
# Without TF_ACC_TERRAFORM_PATH, terraform-plugin-testing downloads Terraform
# through hc-install and checks a GPG signature that breaks when HashiCorp's
# embedded key expires, so point it at the installed tofu.
set -euo pipefail

if [ -z "${TF_ACC_TERRAFORM_PATH:-}" ]; then
  if ! TF_ACC_TERRAFORM_PATH="$(command -v tofu || command -v terraform)"; then
    echo "go-unit-tests: OpenTofu or Terraform is required on PATH." >&2
    exit 1
  fi
fi
export TF_ACC_TERRAFORM_PATH
if [ -z "${TF_ACC_PROVIDER_HOST:-}" ]; then
  case "$(basename "$TF_ACC_TERRAFORM_PATH")" in
    tofu) TF_ACC_PROVIDER_HOST=registry.opentofu.org ;;
    *) TF_ACC_PROVIDER_HOST=registry.terraform.io ;;
  esac
fi
export TF_ACC_PROVIDER_HOST
export TF_ACC_PROVIDER_NAMESPACE=kupecloud

profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT

go test -race -timeout 20m -coverprofile="$profile" ./...
# Coverage is reported, not gated.
go tool cover -func="$profile" | awk 'END {print "total coverage: " $3}'
