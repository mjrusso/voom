#!/usr/bin/env bash
set -euo pipefail

license_root="$(pwd -P)/.release/voom-licenses"
rm -rf "$license_root"

for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  os="${platform%_*}"
  arch="${platform#*_}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go-licenses save ./cmd/voom \
    --save_path="$license_root/$platform"
done
