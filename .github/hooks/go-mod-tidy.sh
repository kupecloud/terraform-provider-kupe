#!/usr/bin/env bash
# Fail when `go mod tidy` would change go.mod or go.sum (the old unit-tests.yaml
# "Check go mod tidy" job). The files are restored afterwards, so the check
# leaves the tree as it found it.
set -euo pipefail

before_dir="$(mktemp -d)"
restore() {
  for file in go.mod go.sum; do
    if [ -f "$before_dir/$file" ]; then
      cp "$before_dir/$file" "$file"
    fi
  done
  rm -rf "$before_dir"
}
trap restore EXIT

for file in go.mod go.sum; do
  if [ -f "$file" ]; then
    cp "$file" "$before_dir/$file"
  fi
done

go mod tidy

changed=0
for file in go.mod go.sum; do
  before_file="$before_dir/$file"
  if [ -f "$before_file" ] && [ -f "$file" ]; then
    if ! cmp -s "$before_file" "$file"; then
      changed=1
      break
    fi
    continue
  fi
  if [ -f "$before_file" ] || [ -f "$file" ]; then
    changed=1
    break
  fi
done

if [ "$changed" -eq 1 ]; then
  echo "go.mod or go.sum is not tidy. Run 'go mod tidy' and commit the changes." >&2
  exit 1
fi
