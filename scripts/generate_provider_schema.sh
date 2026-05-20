#!/usr/bin/env bash

set -euo pipefail

for cmd in go jq; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "Missing required command: ${cmd}" >&2
    exit 1
  fi
done

# Prefer tofu (the project's primary CLI). Fall back to terraform when present
# and usable — some hosts (anything with tfenv intercepting `terraform`)
# require this fallback ordering so the schema dump doesn't fail on the host
# CLI shim.
if command -v tofu >/dev/null 2>&1; then
  TF_BIN="$(command -v tofu)"
elif command -v terraform >/dev/null 2>&1 && terraform version >/dev/null 2>&1; then
  TF_BIN="$(command -v terraform)"
else
  echo "Missing Terraform-compatible CLI: install OpenTofu (https://opentofu.org/) or Terraform" >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="${ROOT_DIR}/.tmp/provider-schema"
BIN_DIR="${TMP_DIR}/bin"
WORK_DIR="${TMP_DIR}/work"
RAW_SCHEMA_PATH="${ROOT_DIR}/.tmp/provider-schema.raw.json"
SCHEMA_PATH="${ROOT_DIR}/.tmp/provider-schema.json"
PROVIDER_BIN="${BIN_DIR}/terraform-provider-kupe"

mkdir -p "${BIN_DIR}" "${WORK_DIR}"

GOCACHE="${GOCACHE:-${ROOT_DIR}/.tmp/go-build}" \
  go build -o "${PROVIDER_BIN}" "${ROOT_DIR}"

CLI_CONFIG_FILE="${TMP_DIR}/terraformrc"
cat > "${CLI_CONFIG_FILE}" <<EOF
provider_installation {
  dev_overrides {
    "kupecloud/kupe" = "${BIN_DIR}"
  }

  direct {
    exclude = ["kupecloud/kupe"]
  }
}
EOF

cat > "${WORK_DIR}/main.tf" <<'EOF'
terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}

provider "kupe" {
  host    = "https://api.kupe.cloud"
  tenant  = "example-tenant"
  api_key = "kupe_example"
}
EOF

(
  cd "${WORK_DIR}"
  TF_CLI_CONFIG_FILE="${CLI_CONFIG_FILE}" "${TF_BIN}" providers schema -json > "${RAW_SCHEMA_PATH}"
)

# Pick whichever registry-namespaced provider schema the CLI emitted (tofu
# uses registry.opentofu.org, terraform uses registry.terraform.io — same
# bytes underneath, just keyed differently).
jq '{
  format_version,
  provider_schemas: {
    "kupe": (
      .provider_schemas["registry.terraform.io/kupecloud/kupe"]
      // .provider_schemas["registry.opentofu.org/kupecloud/kupe"]
    )
  }
}' "${RAW_SCHEMA_PATH}" > "${SCHEMA_PATH}"

echo "Generated ${SCHEMA_PATH}"
