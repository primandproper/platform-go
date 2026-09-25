#!/usr/bin/env bash
set -euo pipefail

# Run tests with coverage
# Usage: coverage.sh [output_file]

OUTPUT_FILE="${1:-coverage.out}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_CONTAINER_TESTS="${RUN_CONTAINER_TESTS:-true}" "${SCRIPT_DIR}/pull_test_containers.sh"

# The conformance tree is excluded, and not because it is slow.
#
# It is this module's promises written as shipped library code, run against a
# server it did not build — so `go test` credits its coverage to whichever
# harness ran it, and the figure says the opposite of the truth: the suite
# asserting against all twelve surfaces reports under 1% while the two that
# happen to sit beside a harness report ~100%. codecov.yml carries the long
# form.
#
# They still run on every pull request, in a workflow of their own, which is
# what keeps this an exclusion from the coverage number rather than from CI.
# See .github/workflows/conformance.yaml.
#
# The per-package timeout is raised from go test's default of ten minutes, which
# nobody chose for this job. It runs every package at once, under the race
# detector, with every container suite sharing one runner's Docker daemon, and
# identity — whose container suites render a fresh schema per subtest — crossed
# ten minutes there once its MySQL suites moved from MariaDB to MySQL 8, whose
# DDL is several times slower. The same package takes under two minutes on a
# developer's machine. Twenty minutes is headroom inside the workflow's own
# forty-five, not a budget for tests to grow into.
#
# shellcheck disable=SC2086,SC2046
CGO_ENABLED=1 go test -shuffle=on -race -vet=all -failfast -timeout 20m -covermode=atomic -coverprofile="${OUTPUT_FILE}" $(go list ./... | grep -Ev '(mock|testutils|/conformance)')
