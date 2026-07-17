#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

expected_module="github.com/SisyphusSQ/mongo-overview-tool/v2"
actual_module="$(go list -m -f '{{.Path}}')"

if [[ "$actual_module" != "$expected_module" ]]; then
  echo "CONTRACT-002: go.mod module path must be $expected_module (actual: $actual_module)" >&2
  exit 1
fi

if ! rg -Fxq "VARS_PKG = $expected_module/vars" Makefile; then
  echo "CONTRACT-002: Makefile VARS_PKG must be $expected_module/vars" >&2
  exit 1
fi

legacy_import_pattern='"github\.com/SisyphusSQ/mongo-overview-tool/(?!v2/)'
if rg -n --pcre2 "$legacy_import_pattern" --glob '*.go' .; then
  echo "CONTRACT-002: Go source contains an import without the /v2 module suffix" >&2
  exit 1
fi

echo "project module contract check passed"
