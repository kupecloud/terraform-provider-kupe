#!/usr/bin/env sh

set -eu

ROOT_DIR=$(
  CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd
)

CLI_CONFIG="$ROOT_DIR/.tmp/tfdevrc"

"$ROOT_DIR/scripts/prepare_local_provider.sh" >/dev/null

cd "$ROOT_DIR/examples"
TF_CLI_CONFIG_FILE="$CLI_CONFIG" tofu validate
