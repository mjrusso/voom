#!/usr/bin/env bash
set -euo pipefail

tag="${1:?usage: scripts/check-release-notes.sh vX.Y.Z [output-file]}"
out="${2:-}"

if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "release tag must look like vX.Y.Z: $tag" >&2
  exit 1
fi

if [ ! -f VERSION ]; then
  echo "VERSION is missing" >&2
  exit 1
fi

version="$(< VERSION)"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "VERSION must contain X.Y.Z: $version" >&2
  exit 1
fi

if ! printf '%s\n' "$version" | cmp -s - VERSION; then
  echo "VERSION must contain exactly one X.Y.Z line" >&2
  exit 1
fi

if [ "$tag" != "v$version" ]; then
  echo "release tag $tag does not match VERSION ($version)" >&2
  exit 1
fi

if [ ! -f CHANGELOG.md ]; then
  echo "CHANGELOG.md is missing" >&2
  exit 1
fi

if ! awk -v tag="$tag" '
  BEGIN {
    header = "## " tag " - "
  }
  index($0, header) == 1 {
    date = substr($0, length(header) + 1)
    if (date ~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/) {
      found = 1
    }
  }
  END {
    exit found ? 0 : 1
  }
' CHANGELOG.md; then
  echo "CHANGELOG.md must contain a section header like: ## ${tag} - YYYY-MM-DD" >&2
  exit 1
fi

notes="$(
  awk -v tag="$tag" '
    BEGIN {
      header = "## " tag " - "
    }
    index($0, header) == 1 {
      date = substr($0, length(header) + 1)
      if (date !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/) {
        next
      }
      in_section = 1
      next
    }
    in_section && /^## / {
      exit
    }
    in_section {
      print
    }
  ' CHANGELOG.md
)"

if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md section for ${tag} has no release notes" >&2
  exit 1
fi

if [ -n "$out" ]; then
  mkdir -p "$(dirname "$out")"
  printf '%s\n' "$notes" > "$out"
fi
