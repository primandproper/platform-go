#!/usr/bin/env bash
set -euo pipefail

# Check every generated .sql in this module against the schema it is written for,
# with sqlc.
# Usage: sqlc_compile.sh <project_root>
#
# `sqlc compile` parses and type-checks the queries against the DDL and emits
# nothing, which is the half of sqlc this check wants: the answer to "does every
# committed statement still fit the schema", with no database running and no
# generated package to compare against. What a store executes is generated
# separately, by `make unison`, and the generated-files job diffs that; this is
# the faster gate that fails on a renamed column without installing a plugin.
#
# Neither input is written by hand. The queries come from `make generate`, and a
# hand-edit of one is caught by the regeneration diff rather than here; the
# schema is rendered on the fly by the same command, at the empty table prefix,
# because sqlc resolves table names when it runs and an identifier is not a bind
# parameter in any of the three engines.

PROJECT_ROOT="${1:-$(pwd)}"

SQLC_VERSION="$(cat "${PROJECT_ROOT}/.sqlc-version")"

# The components checked, as
# "<module-relative package> <generator> <queries directory> <dialect>...".
# Each package's directory holds one <dialect>_generated.sql per dialect it
# claims, and its `-schema <dialect>` mode prints the DDL those queries are read
# against.
#
# The dialects are per component rather than a list of their own, because a
# roster is a property of the package: identity serves all three, while
# operations, timers and workqueue serve Postgres alone for the reasons their
# own docs give, and checking a package against a dialect it refuses to run on
# would be checking SQL nobody will ever execute. Each list has to match the
# keys of that package's unison.yaml `schemas:` map.
COMPONENTS=(
  "./identity ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./audit ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./authentication/oauth2serverstore ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./authentication/oauth2clients ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./authentication/passwordreset ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./authentication/webauthncredentials ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./rbac ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./billing ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./comments ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./shredding ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./dataprivacy ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./issuereports ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./links/database ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./metering ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./notifications ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./saga ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./sessions/database ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./settings ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./mediaregistry ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./waitlists ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./webhooks ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./outbox ./internal/queriesgen internal/queries postgres mysql sqlite"
  "./operations ./internal/queriesgen internal/queries postgres"
  "./timers ./internal/queriesgen internal/queries postgres"
  "./workqueue ./internal/queriesgen internal/queries postgres"
)

# The sqlc engine each dialect this module names is analyzed by. A generator
# that emits SQL the engine cannot parse is a generator with a broken dialect,
# not a check with a gap.
engine_for() {
  case "${1}" in
    postgres) echo "postgresql" ;;
    mysql) echo "mysql" ;;
    sqlite) echo "sqlite" ;;
    *)
      echo "unknown dialect ${1}" >&2
      exit 1
      ;;
  esac
}

ensure_sqlc() {
  "${PROJECT_ROOT}/.scripts/ensure_tool_installed.sh" sqlc \
    "go install github.com/sqlc-dev/sqlc/cmd/sqlc@v${SQLC_VERSION}"

  local installed
  installed="$(sqlc version)"

  # A pin nothing enforces is a version number in a file. ensure_tool_installed
  # only installs what is missing, so an older sqlc already on PATH would
  # otherwise decide what this check means.
  if [ "${installed}" != "v${SQLC_VERSION}" ]; then
    echo "sqlc ${installed} is on PATH; installing the pinned v${SQLC_VERSION}"
    go install "github.com/sqlc-dev/sqlc/cmd/sqlc@v${SQLC_VERSION}"
  fi
}

main() {
  ensure_sqlc

  local workspace
  workspace="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '${workspace}'" EXIT

  local config="${workspace}/sqlc.yaml"
  printf 'version: "2"\nsql:\n' > "${config}"

  local component package generator queries_dir d engine target
  for component in "${COMPONENTS[@]}"; do
    # shellcheck disable=SC2086
    set -- ${component}
    package="${1}"
    generator="${2}"
    queries_dir="${3}"
    shift 3

    for d in "$@"; do
      engine="$(engine_for "${d}")"

      target="${workspace}/${package//\//_}_${d}"
      mkdir -p "${target}"

      (cd "${PROJECT_ROOT}/${package}" && go run "${generator}" -schema "${d}") > "${target}/schema.sql"
      cp "${PROJECT_ROOT}/${package}/${queries_dir}/${d}_generated.sql" "${target}/queries.sql"

      cat >> "${config}" <<EOF
  - engine: ${engine}
    schema: $(basename "${target}")/schema.sql
    queries: $(basename "${target}")/queries.sql
    gen:
      go:
        package: checked
        out: $(basename "${target}")/gen
EOF
    done
  done

  (cd "${workspace}" && sqlc compile)

  echo "sqlc v${SQLC_VERSION}: every checked dialect compiles"
}

main
