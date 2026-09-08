#!/usr/bin/env bash
set -euo pipefail

# Regenerate every sqlc-gen-unison-generated package in this module.
# Usage: unison_generate.sh <project_root>
#
# unison is the emitter, never the analyzer: it shells out to the pinned sqlc
# once per dialect and turns the analysis into one shared set of Go types plus
# per-dialect statements. Both halves of the toolchain are pinned — sqlc by
# .sqlc-version, unison by .unison-version — because an unpinned either one
# turns the generated-files check into a report on which machine ran it. The
# pins are enforced right here, by installing exactly what they name: the
# version header unison writes carries only the sqlc pin, since a go-installed
# unison stamps `dev` — its release artifacts alone carry a real version.

PROJECT_ROOT="${1:-$(pwd)}"

UNISON_VERSION="$(cat "${PROJECT_ROOT}/.unison-version")"
SQLC_VERSION="$(cat "${PROJECT_ROOT}/.sqlc-version")"

BIN_DIR="${PROJECT_ROOT}/artifacts/bin"
UNISON="${BIN_DIR}/unison"

# sqlc, pinned the way sqlc_compile.sh pins it: installed when absent, and
# reinstalled when the one on PATH is not the pinned release.
"${PROJECT_ROOT}/.scripts/ensure_tool_installed.sh" sqlc \
  "go install github.com/sqlc-dev/sqlc/cmd/sqlc@v${SQLC_VERSION}"

installed="$(sqlc version 2>/dev/null || echo none)"
if [ "${installed}" != "v${SQLC_VERSION}" ]; then
  echo "sqlc ${installed} is on PATH; installing the pinned v${SQLC_VERSION}"
  go install "github.com/sqlc-dev/sqlc/cmd/sqlc@v${SQLC_VERSION}"
fi

# unison, installed unconditionally at the pin — a pin nothing enforces is a
# version number in a file — into the gitignored artifacts/ rather than onto
# the developer's PATH. The command lives at cmd/unison, so `go install` names
# the binary for its package directory and there is nothing to rename.
mkdir -p "${BIN_DIR}"
GOBIN="${BIN_DIR}" go install "github.com/primandproper/sqlc-gen-unison/cmd/unison@v${UNISON_VERSION}"

# The components generated, as "<package dir> <dialect>...". Each holds a
# unison.yaml, and its per-dialect schema files are rendered first, from the
# package's own migrations at the empty table prefix — the single source, so
# there is no hand-written schema copy to drift. unison substitutes the
# consumer's real prefix at construction; sqlc analyzes the canonical names
# because an identifier is not a bind parameter in any of the three engines.
#
# The dialects are per component rather than a list up here, because a roster is
# a property of the package rather than of this script: operations is
# Postgres-only for reasons its own doc gives, and rendering it a MySQL schema
# would be rendering a schema for a database it refuses to run against. Each
# component's list has to match the keys of its unison.yaml `schemas:` map,
# which is what unison itself reads the roster from.
COMPONENTS=(
  "identity postgres mysql sqlite"
  "audit postgres mysql sqlite"
  "authentication/oauth2server/database postgres mysql sqlite"
  "authentication/oauth2clients postgres mysql sqlite"
  "authentication/passwordreset postgres mysql sqlite"
  "authentication/webauthn/database postgres mysql sqlite"
  "authorization/database postgres mysql sqlite"
  "billing postgres mysql sqlite"
  "comments postgres mysql sqlite"
  "cryptography/shredding postgres mysql sqlite"
  "dataprivacy postgres mysql sqlite"
  "issuereports postgres mysql sqlite"
  "links/database postgres mysql sqlite"
  "metering postgres mysql sqlite"
  "notifications postgres mysql sqlite"
  "saga postgres mysql sqlite"
  "sessions/database postgres mysql sqlite"
  "settings postgres mysql sqlite"
  "uploads/registry postgres mysql sqlite"
  "waitlists postgres mysql sqlite"
  "webhooks postgres mysql sqlite"
  "outbox postgres mysql sqlite"
  "operations postgres"
  "timers postgres"
  "workqueue postgres"
)

for component in "${COMPONENTS[@]}"; do
  # shellcheck disable=SC2086
  set -- ${component}
  package="${1}"
  shift

  for d in "$@"; do
    (cd "${PROJECT_ROOT}" &&
      go run "./${package}/internal/queriesgen" -schema "${d}" \
        > "${package}/migrations/schema/${d}.sql")
  done

  (cd "${PROJECT_ROOT}/${package}" && "${UNISON}" generate)
done

echo "unison v${UNISON_VERSION}: every generated package is current"
