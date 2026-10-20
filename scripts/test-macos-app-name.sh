#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != Darwin ]]; then
  echo "Testing app registration requires a logged-in MacOS session and Apple's SDK." >&2
  exit 2
fi

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$repo_root"
VOOM_TEST_GUI=1 go test ./internal/process ./internal/vm \
  -run '^(TestGUIAppRegistersPerVMNames|TestRecordedProcessKindsWithSpacedExecutablePath|TestPrepareGUIAppSupportsLongNamesAndRename)$' \
  -count=1 -v
