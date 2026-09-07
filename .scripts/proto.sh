#!/usr/bin/env bash
set -euo pipefail

# Generate the Go bindings for the .proto files this module ships.
# Usage: proto.sh <project_root>
#
# Every part of the toolchain is pinned, because the generated files are checked
# in and CI fails on a dirty tree. Both plugins stamp their own version and
# protoc's into every file they write, so an unpinned any one of them turns that
# check into a report on which machine ran it.
#
#   - protoc-gen-go emits the message types, and protoc-gen-go-grpc the service
#     stubs. Both are pinned by go.mod, via the tool directive, and built from
#     the module cache rather than installed. `go tool` cannot be used directly
#     here: protoc runs its plugins as executables it finds itself.
#   - protoc is pinned by .protoc-version, and lives in .protoc-version and
#     nowhere else. A protoc already on PATH is used when it matches; otherwise
#     the pinned release is downloaded under artifacts/, which is gitignored.
#
# protoc-gen-go-grpc writes nothing for a .proto with no service in it, so a
# schema-only file like filtering's gains no second output by this plugin being
# on the command line.
#
# The generated files are formatted afterwards by `make format` rather than
# here, so `make proto format` is what a contributor runs and what CI runs.
# Nothing in this script formats, so its output is exactly protoc's.

PROJECT_ROOT="${1:-$(pwd)}"

PROTOC_VERSION="$(tr -d '[:space:]' < "${PROJECT_ROOT}/.protoc-version")"
TOOL_DIR="${PROJECT_ROOT}/artifacts/proto"
PROTOC_DIR="${TOOL_DIR}/protoc-${PROTOC_VERSION}"

mkdir -p "${TOOL_DIR}"

# Resolve protoc: the one on PATH if it is the pinned version, else a pinned
# download of our own.
protoc_bin=""
if command -v protoc &> /dev/null && [ "$(protoc --version)" = "libprotoc ${PROTOC_VERSION}" ]; then
  protoc_bin="$(command -v protoc)"
elif [ -x "${PROTOC_DIR}/bin/protoc" ]; then
  protoc_bin="${PROTOC_DIR}/bin/protoc"
else
  case "$(uname -s)" in
    Darwin) os="osx" ;;
    Linux) os="linux" ;;
    *)
      echo "proto.sh: no pinned protoc for $(uname -s); install protoc ${PROTOC_VERSION} and put it on PATH" >&2
      exit 1
      ;;
  esac

  case "$(uname -m)" in
    x86_64 | amd64) arch="x86_64" ;;
    arm64 | aarch64) arch="aarch_64" ;;
    *)
      echo "proto.sh: no pinned protoc for $(uname -m); install protoc ${PROTOC_VERSION} and put it on PATH" >&2
      exit 1
      ;;
  esac

  archive="protoc-${PROTOC_VERSION}-${os}-${arch}.zip"
  url="https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/${archive}"

  echo "proto.sh: fetching ${url}"
  rm -rf "${PROTOC_DIR}"
  mkdir -p "${PROTOC_DIR}"
  curl --fail --location --silent --show-error --output "${TOOL_DIR}/${archive}" "${url}"
  unzip -q -o "${TOOL_DIR}/${archive}" -d "${PROTOC_DIR}"
  rm -f "${TOOL_DIR}/${archive}"

  protoc_bin="${PROTOC_DIR}/bin/protoc"
fi

# protoc runs its plugins as executables, so each tool directive has to be built
# out to a file rather than invoked through `go tool`.
go_plugin="${TOOL_DIR}/protoc-gen-go"
grpc_plugin="${TOOL_DIR}/protoc-gen-go-grpc"
(cd "${PROJECT_ROOT}" && go build -o "${go_plugin}" google.golang.org/protobuf/cmd/protoc-gen-go)
(cd "${PROJECT_ROOT}" && go build -o "${grpc_plugin}" google.golang.org/grpc/cmd/protoc-gen-go-grpc)

# The module path is derived rather than written down, so a major version bump
# does not leave this script pointing at the previous one. The go_package
# options in the .proto files still carry it, and still have to be updated.
module_path="$(cd "${PROJECT_ROOT}" && go list -m)"

# One proto_path per directory a .proto tree is rooted at, so files are
# addressed by the canonical import path a consumer would use rather than by
# where they happen to sit in this repository.
# Relative to the project root rather than absolute, because an absolute path
# carries whatever the checkout happens to sit under -- a worktree beneath a
# dot-directory, say -- and the hidden-directory exclusion would then discard
# the whole tree.
proto_roots=()
while IFS= read -r -d '' dir; do
  proto_roots+=("${dir#./}")
done < <(cd "${PROJECT_ROOT}" && find . -type d -name proto -not -path './artifacts/*' -not -path './.*' -print0 | sort -z)

if [ ${#proto_roots[@]} -eq 0 ]; then
  echo "proto.sh: no proto/ directories found under ${PROJECT_ROOT}" >&2
  exit 1
fi

path_args=()
proto_files=()
for root in "${proto_roots[@]}"; do
  path_args+=("--proto_path=${root}")

  while IFS= read -r -d '' file; do
    proto_files+=("${file}")
  done < <(cd "${PROJECT_ROOT}" && find "${root}" -type f -name '*.proto' -print0 | sort -z)
done

if [ ${#proto_files[@]} -eq 0 ]; then
  echo "proto.sh: no .proto files found under ${PROJECT_ROOT}" >&2
  exit 1
fi

# --go_opt=module and --go-grpc_opt=module strip the module path off each file's
# go_package and write what remains, so a file's output path is decided by its
# own go_package option and not by an argument here. Both plugins are given the
# same one, so the messages and the stubs land in the same package.
#
# require_unimplemented_servers is left at its default of true: an
# implementation embeds the generated Unimplemented struct, and an RPC added to
# a service later is then additive rather than a compile failure in every
# consumer that had implemented the interface exhaustively.
(cd "${PROJECT_ROOT}" && "${protoc_bin}" \
  "${path_args[@]}" \
  --plugin="protoc-gen-go=${go_plugin}" \
  --plugin="protoc-gen-go-grpc=${grpc_plugin}" \
  --go_out=. \
  --go_opt="module=${module_path}" \
  --go-grpc_out=. \
  --go-grpc_opt="module=${module_path}" \
  "${proto_files[@]}")
