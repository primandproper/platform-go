#!/usr/bin/env bash
set -euo pipefail

# Format Go imports using gci
# Usage: format_imports.sh <package_prefix> <project_root>

PACKAGE_PREFIX="${1:-github.com/primandproper/platform-go}"
PROJECT_ROOT="${2:-$(pwd)}"

# Find all Go files and pass them to gci
go_files=()
while IFS= read -r -d '' file; do
  go_files+=("${file}")
done < <(find "${PROJECT_ROOT}" -type f -not -path '*/vendor/*' -name "*.go" -print0)

if [ ${#go_files[@]} -gt 0 ]; then
  go tool gci write \
    --section standard \
    --section "prefix(${PACKAGE_PREFIX})" \
    --section "prefix($(dirname "${PACKAGE_PREFIX}"))" \
    --section default \
    --custom-order \
    "${go_files[@]}"
fi
