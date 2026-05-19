#!/usr/bin/env sh

set -eu

ROOT_DIR=$(
  CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd
)

GOOS=$(go env GOOS)
GOARCH=$(go env GOARCH)
PLUGIN_DIR="$ROOT_DIR/.tmp/plugins/registry.terraform.io/kupecloud/kupe/dev/${GOOS}_${GOARCH}"
CLI_CONFIG="$ROOT_DIR/.tmp/tfdevrc"
PROVIDER_BIN="$PLUGIN_DIR/terraform-provider-kupe"

mkdir -p "$PLUGIN_DIR"
go build -o "$PROVIDER_BIN" "$ROOT_DIR"

cat >"$CLI_CONFIG" <<EOF
provider_installation {
  dev_overrides {
    "kupecloud/kupe" = "$PLUGIN_DIR"
  }

  direct {}
}
EOF

printf 'Built local provider binary: %s\n\n' "$PROVIDER_BIN"

cat <<EOF
Run this in your shell to point Terraform/OpenTofu at the local binary:

  export TF_CLI_CONFIG_FILE=$CLI_CONFIG

The path above is absolute, so it works from any cwd. With the dev override
active you should NOT run \`tofu init\` — apply directly. If you ever see
"Inconsistent dependency lock file" or "provider not found in registry"
errors, an earlier \`tofu init\` left a stale lock; clean it with:

  rm -f .terraform.lock.hcl
  rm -rf .terraform

EOF
