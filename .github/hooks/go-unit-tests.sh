#!/usr/bin/env bash
# Unit and mock-backed acceptance tests, as the old unit-tests.yaml ran them:
# `go test -race -coverprofile` with OpenTofu as the acceptance-test CLI.
# Without TF_ACC_TERRAFORM_PATH, terraform-plugin-testing downloads Terraform
# through hc-install and checks a GPG signature that breaks when HashiCorp's
# embedded key expires, so point it at the installed tofu.
set -euo pipefail

if ! TF_ACC_TERRAFORM_PATH="$(command -v tofu)"; then
  echo "go-unit-tests: OpenTofu (tofu) is not on PATH; install it first." >&2
  exit 1
fi
export TF_ACC_TERRAFORM_PATH
export TF_ACC_PROVIDER_HOST=registry.opentofu.org
export TF_ACC_PROVIDER_NAMESPACE=kupecloud

profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT

go test -race -coverprofile="$profile" ./...
# Coverage is reported, not gated.
go tool cover -func="$profile" | awk 'END {print "total coverage: " $3}'
