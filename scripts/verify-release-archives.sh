#!/usr/bin/env bash
set -euo pipefail

dist="${1:-dist}"
expected=(
  voom_darwin_arm64.tar.gz
  voom_darwin_x64.tar.gz
  voom_linux_arm64.tar.gz
  voom_linux_x64.tar.gz
  checksums.txt
)

for f in "${expected[@]}"; do
  test -f "$dist/$f"
done

(cd "$dist" && sha256sum -c checksums.txt)

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for archive in "$dist"/voom_*.tar.gz; do
  rm -rf "$tmp"/*
  tar -xzf "$archive" -C "$tmp"
  test -f "$tmp/LICENSE"
  test -f "$tmp/README.md"
  test -f "$tmp/CHANGELOG.md"
  test -x "$tmp/voom"
done
