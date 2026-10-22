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
  rm -rf "${tmp:?}"/*
  tar -xzf "$archive" -C "$tmp"
  test -f "$tmp/LICENSE"
  test -f "$tmp/LICENSE.gvproxy"
  test -f "$tmp/NOTICE.gvproxy"
  test -f "$tmp/THIRD_PARTY_LICENSES/gopkg.in/yaml.v3/NOTICE"
  test -f "$tmp/THIRD_PARTY_LICENSES/gvisor.dev/gvisor/pkg/LICENSE"
  platform="${archive##*/}"
  platform="${platform#voom_}"
  platform="${platform%.tar.gz}"
  platform="${platform/_x64/_amd64}"
  CGO_ENABLED=0 GOOS="${platform%_*}" GOARCH="${platform#*_}" \
    go-licenses save ./cmd/voom --save_path="$tmp/voom-licenses"
  diff -r "$tmp/voom-licenses" "$tmp/THIRD_PARTY_LICENSES.voom"
  test -f "$tmp/README.md"
  test -f "$tmp/CHANGELOG.md"
  for doc in CONTRIBUTING.md assets/voom.png docs/installation.md \
    docs/linux-guests.md docs/macos-guests.md docs/proxy-attachments.md \
    docs/reference.md docs/third-party-licenses.md docs/commands/voom.md \
    nix/GVPROXY.md nix/NOTICE.gvproxy nix/NOTICE.vfkit go.mod go.sum; do
    test -f "$tmp/$doc"
  done
  test -x "$tmp/voom"
  test -x "$tmp/gvproxy"
  if [[ "$archive" == */voom_darwin_*.tar.gz ]]; then
    test -x "$tmp/vfkit"
    test -f "$tmp/LICENSE.vfkit"
    test -f "$tmp/NOTICE.vfkit"
    test -f "$tmp/THIRD_PARTY_LICENSES.vfkit/github.com/Code-Hex/vz/v3/LICENSE"
  else
    test ! -e "$tmp/vfkit"
  fi
  if [[ "$archive" == */voom_darwin_arm64.tar.gz ]]; then
    test -x "$tmp/voom-macos-identity"
  else
    test ! -e "$tmp/voom-macos-identity"
  fi
done
