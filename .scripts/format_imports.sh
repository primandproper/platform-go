#!/usr/bin/env bash
set -euo pipefail

# Format Go imports using gci
# Usage: format_imports.sh <package_prefix> <project_root>

PACKAGE_PREFIX="${1:-$(go list -m)}"
PROJECT_ROOT="${2:-$(pwd)}"

# Org prefix is the module path's parent, ignoring any major-version suffix
# (e.g. /v2). Without stripping it, dirname would yield the un-suffixed module
# path instead of the org root.
ORG_PREFIX="$(dirname "${PACKAGE_PREFIX%/v[0-9]*}")"

# Find all Go files and pass them to gci
go_files=()
while IFS= read -r -d '' file; do
  go_files+=("${file}")
done < <(find "${PROJECT_ROOT}" -type f -not -path '*/vendor/*' -name "*.go" -print0)

if [ ${#go_files[@]} -gt 0 ]; then
  go tool gci write \
    --section standard \
    --section "prefix(${PACKAGE_PREFIX})" \
    --section "prefix(${ORG_PREFIX})" \
    --section default \
    --custom-order \
    "${go_files[@]}"
fi
