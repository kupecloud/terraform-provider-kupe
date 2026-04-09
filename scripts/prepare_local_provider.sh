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

printf 'Built local provider binary: %s\n' "$PROVIDER_BIN"
printf 'Use TF_CLI_CONFIG_FILE=%s for local Terraform or OpenTofu testing.\n' "$CLI_CONFIG"
