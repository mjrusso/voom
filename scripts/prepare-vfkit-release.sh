#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != Darwin ]]; then
  echo "Building vfkit releases requires a Mac and Apple's SDK." >&2
  exit 1
fi

source_path="$(nix build path:.#vfkit-source --no-link --print-out-paths)"
release_root="$(pwd -P)/.release"
source_target="$release_root/vfkit"
license_root="$release_root/vfkit-licenses"
rm -rf "$source_target" "$license_root"
mkdir -p "$source_target"
cp -R "$source_path/." "$source_target/"
chmod -R u+w "$source_target"

export DEVELOPER_DIR SDKROOT
DEVELOPER_DIR="$(/usr/bin/xcode-select --print-path)"
SDKROOT="$(/usr/bin/xcrun --sdk macosx --show-sdk-path)"
compiler="$(/usr/bin/xcrun --sdk macosx --find clang)"
export CC="$compiler"
export CGO_ENABLED=1
export CGO_CFLAGS="-isysroot $SDKROOT -mmacosx-version-min=13.0"
export CGO_LDFLAGS="-isysroot $SDKROOT"
export GOFLAGS=-mod=vendor

(
  cd "$source_target"
  go test ./pkg/cmdline ./pkg/rest ./pkg/util
  go test ./pkg/config -run 'TestVirtioFsReadOnlyRoundTrip|TestJSON'
  go-licenses save ./cmd/vfkit --save_path="$license_root"
  for arch in arm64 amd64; do
    output="$release_root/native/darwin_$arch"
    mkdir -p "$output"
    GOOS=darwin GOARCH="$arch" go build -o "$output/vfkit" ./cmd/vfkit
    /usr/bin/codesign --force --entitlements vf.entitlements --sign - "$output/vfkit"
    /usr/bin/codesign --verify --strict "$output/vfkit"
    cp LICENSE "$output/LICENSE.vfkit"
    cp "$release_root/../nix/NOTICE.vfkit" "$output/NOTICE.vfkit"
    rm -rf "$output/THIRD_PARTY_LICENSES.vfkit"
    cp -R "$license_root" "$output/THIRD_PARTY_LICENSES.vfkit"
  done
)
