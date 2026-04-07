#!/usr/bin/env bash

set -euo pipefail

for cmd in go terraform jq; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "Missing required command: ${cmd}" >&2
    exit 1
  fi
done

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
  TF_CLI_CONFIG_FILE="${CLI_CONFIG_FILE}" terraform providers schema -json > "${RAW_SCHEMA_PATH}"
)

jq '{
  format_version,
  provider_schemas: {
    "kupe": .provider_schemas["registry.terraform.io/kupecloud/kupe"]
  }
}' "${RAW_SCHEMA_PATH}" > "${SCHEMA_PATH}"

echo "Generated ${SCHEMA_PATH}"
