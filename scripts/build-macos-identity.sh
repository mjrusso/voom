#!/usr/bin/env bash
set -euo pipefail

output="${1:-bin/voom-macos-identity}"
if [[ "$(uname -s)" != Darwin ]]; then
  echo "Building the MacOS identity helper requires a Mac and Apple's SDK." >&2
  exit 1
fi
mkdir -p "$(dirname "$output")"
xcrun clang -arch arm64 -fobjc-arc -Wall -Wextra -Werror \
  -mmacosx-version-min=12.0 -framework Foundation -framework Virtualization \
  native/macos-identity/main.m -o "$output"
codesign --force --sign - "$output"
if [[ "$(uname -m)" == arm64 ]]; then
  "$output" --self-test
fi
