#!/usr/bin/env bash
set -euo pipefail

# Run tests
# Usage: test.sh
RUN_CONTAINER_TESTS="${1:-true}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_CONTAINER_TESTS="${RUN_CONTAINER_TESTS}" "${SCRIPT_DIR}/pull_test_containers.sh"

# shellcheck disable=SC2086,SC2046
CGO_ENABLED=1 RUN_CONTAINER_TESTS=${RUN_CONTAINER_TESTS} go test -shuffle=on -race -vet=all -failfast $(go list ./... | grep -Ev '(cmd|integration|mock|fakes|converters|utils|generated)')
