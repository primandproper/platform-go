#!/usr/bin/env bash
set -euo pipefail

# Run all Go benchmarks and regenerate the BENCHMARKS.md reference table.
# Usage: benchmark.sh
#
# Env:
#   BENCHTIME            passed to `go test -benchtime` (default: 1s)
#   BENCH_PKG            restrict to a single package dir, e.g. ./cryptography/hashing
#   RUN_CONTAINER_TESTS  "true" includes infra-backed (testcontainer) benchmarks
#   OUTPUT_FILE          markdown output path (default: BENCHMARKS.md)
#
# Benchmarks run WITHOUT -race on purpose: the race detector badly skews timings.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${PROJECT_ROOT}"

BENCHTIME="${BENCHTIME:-1s}"
OUTPUT_FILE="${OUTPUT_FILE:-BENCHMARKS.md}"
RUN_CONTAINER_TESTS="${RUN_CONTAINER_TESTS:-false}"

# Discover the package dirs that actually contain benchmark functions so we
# don't pay test-binary compile cost for packages that have none.
pkgs=()
if [[ -n "${BENCH_PKG:-}" ]]; then
  pkgs=("${BENCH_PKG}")
else
  # `mapfile` is bash 4+; read in a loop so this works on macOS bash 3.2 too.
  while IFS= read -r pkg; do
    pkgs+=("${pkg}")
  done < <(
    grep -rlE '^func Benchmark' --include='*_test.go' . |
      grep -v '/vendor/' |
      sed 's#/[^/]*$##' |
      sort -u |
      grep -Ev '(/mock|/fakes|/converters|/utils|/generated)'
  )
fi

if [[ ${#pkgs[@]} -eq 0 ]]; then
  echo "benchmark.sh: no packages with benchmarks found" >&2
fi

# Pre-pull container images when infra-backed benchmarks are enabled, mirroring
# test.sh, so the first benchmark isn't dominated by an image pull.
if [[ "${RUN_CONTAINER_TESTS}" == "true" ]]; then
  RUN_CONTAINER_TESTS="${RUN_CONTAINER_TESTS}" "${SCRIPT_DIR}/pull_test_containers.sh"
fi

# Flag packages whose benchmarks gate on testcontainers (via
# containers.SkipIfNotRunning) so the report can mark them as such. A package
# qualifies only when a file that defines a benchmark also gates on containers —
# this avoids flagging packages (e.g. cache/redis/slots) whose *tests* use a
# container but whose *benchmarks* are pure-CPU.
container_pkgs=""
for pkg in "${pkgs[@]}"; do
  while IFS= read -r bench_file; do
    if grep -q 'SkipIfNotRunning' "${bench_file}"; then
      container_pkgs="${container_pkgs:+${container_pkgs},}${pkg#./}"
      break
    fi
  done < <(grep -lE '^func Benchmark' "${pkg}"/*_test.go 2>/dev/null)
done

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

for pkg in "${pkgs[@]}"; do
  echo "==> benchmarking ${pkg}" >&2
  # A failing package shouldn't abort the whole run; capture what we can.
  CGO_ENABLED=1 RUN_CONTAINER_TESTS="${RUN_CONTAINER_TESTS}" \
    go test -run='^$' -bench=. -benchmem -benchtime="${BENCHTIME}" "${pkg}" |
    tee -a "${tmp}" || echo "benchmark.sh: ${pkg} failed" >&2
done

go run ./internal/cmd/benchtable -out "${OUTPUT_FILE}" -containers "${container_pkgs}" <"${tmp}"
