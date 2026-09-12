#!/usr/bin/env bash
set -euo pipefail

source_target=".release/gvproxy"
license_root="$(pwd -P)/.release/gvproxy-licenses"
rm -rf "$source_target" "$license_root"
source_path="$(nix build path:.#gvproxy-source --no-link --print-out-paths)"

mkdir -p "$source_target" "$license_root"
cp -R "$source_path/." "$source_target/"
chmod -R u+w "$source_target"

for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  os="${platform%_*}"
  arch="${platform#*_}"
  (
    cd "$source_target"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go-licenses check ./cmd/gvproxy \
      --allowed_licenses=Apache-2.0,MIT,BSD-3-Clause
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go-licenses save ./cmd/gvproxy \
      --save_path="$license_root/$platform"
  )
done
