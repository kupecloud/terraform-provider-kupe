#!/usr/bin/env bash
#
# go-env.sh: run a pre-commit hook's command with the Go environment make
# uses, so a hook checks on the same Go as `make lint` and CI. `make go-env`
# prints it: GOTOOLCHAIN from go.mod and the checkout's golangci-lint cache,
# each kept when the caller set it (CI's setup-go sets GOTOOLCHAIN=local, so
# CI never downloads a Go). A `gofmt` command runs that Go's gofmt: the gofmt
# on PATH belongs to the host Go and GOTOOLCHAIN does not switch it.
#
# Usage: bash .github/hooks/go-env.sh <command> [args...]
set -euo pipefail

[ "$#" -gt 0 ] || {
  echo "usage: go-env.sh <command> [args...]" >&2
  exit 2
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

go_env="$(make --no-print-directory -s -C "$ROOT" go-env)"
while IFS='=' read -r name value; do
  [ -n "$name" ] || continue
  case "$name" in
    GOTOOLCHAIN | GOLANGCI_LINT_CACHE) export "$name=$value" ;;
    *)
      echo "go-env.sh: unexpected make go-env line: $name" >&2
      exit 2
      ;;
  esac
done <<<"$go_env"

if [ "$1" = gofmt ]; then
  shift
  goroot="$(go env GOROOT)"
  set -- "$goroot/bin/gofmt" "$@"
fi
exec "$@"
