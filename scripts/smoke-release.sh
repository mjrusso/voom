#!/usr/bin/env bash
set -euo pipefail

dist="${1:-dist}"

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *)
    echo "unsupported release smoke OS: $(uname -s)" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  arm64 | aarch64) arch="arm64" ;;
  x86_64 | amd64) arch="x64" ;;
  *)
    echo "unsupported release smoke architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

archive="$dist/voom_${os}_${arch}.tar.gz"

test -f "$archive"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

tar -xzf "$archive" -C "$tmp"
test -x "$tmp/voom"
test -x "$tmp/gvproxy"

"$tmp/voom" version
"$tmp/voom" --help >/dev/null
"$tmp/gvproxy" -capabilities | grep -q '"guest-isolation-v1":1'
"$tmp/gvproxy" -capabilities | grep -q '"udp-forward-v1":1'

if [[ "$os" == darwin ]]; then
  "$tmp/vfkit" --capabilities | grep -q '"gui-lifecycle-v1":1'
  "$tmp/vfkit" --capabilities | grep -q '"gui-branding-v1":1'
  "$tmp/vfkit" --capabilities | grep -q '"virtiofs-readonly-v1":1'
  /usr/bin/codesign --verify --strict "$tmp/vfkit"
  /usr/bin/codesign --display --entitlements :- "$tmp/vfkit" 2>/dev/null | grep -q '<key>com.apple.security.virtualization</key>'
fi

if [[ "$os/$arch" == darwin/arm64 ]]; then
  "$tmp/voom-macos-identity" --self-test
fi
