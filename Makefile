# ENVIRONMENT
PWD := $(shell pwd)

# PATHS
THIS          := github.com/primandproper/platform-go/v15
ARTIFACTS_DIR := artifacts
SCRIPTS_DIR   := .scripts

# COMPUTED
#
# Deferred, not `:=`. Immediate expansion runs `go list` when the Makefile is
# *read*, so every target pays for it; only build reads this.
TOTAL_PACKAGE_LIST = $(shell go list $(THIS)/...)

# CONTAINER VERSIONS
#
# The linter version lives in .golangci-version and nowhere else. The lint
# workflow reads the same file, because it wants a bare version where this
# wants an image reference — two spellings of one fact, which is how the two
# used to drift apart.
LINTER_VERSION   := $(shell cat .golangci-version)
LINTER_IMAGE     := golangci/golangci-lint:$(LINTER_VERSION)
SHELLCHECK_IMAGE := koalaman/shellcheck:stable

# COMMANDS
CONTAINER_RUNNER      := docker
RUN_CONTAINER         := $(CONTAINER_RUNNER) run --rm --volume $(PWD):$(PWD) --workdir=$(PWD) --network=host
# RUN_CONTAINER mounts only $(PWD), so a containerised tool has neither module
# cache nor build cache and would re-resolve the whole graph on every run. A
# vendor/ directory used to hide that; this is the part of it worth keeping.
# It lives outside $(PWD) so no tool walks it as source.
GO_CACHE              ?= $(HOME)/.cache/platform-go-go
RUN_CONTAINER_CACHED  := $(RUN_CONTAINER) \
	--volume $(GO_CACHE):/gocache \
	--env GOCACHE=/gocache/build \
	--env GOMODCACHE=/gocache/mod
LINTER                := $(RUN_CONTAINER_CACHED) $(LINTER_IMAGE) golangci-lint

## non-PHONY folders/files

$(ARTIFACTS_DIR):
	@mkdir -p $(ARTIFACTS_DIR)

## PREREQUISITES

.PHONY: setup
setup: $(ARTIFACTS_DIR)
	go mod download

## FORMATTING

.PHONY: format_imports
format_imports:
	$(SCRIPTS_DIR)/format_imports.sh $(THIS) $(PWD)

.PHONY: format_go_fieldalignment
format_go_fieldalignment:
	@$(SCRIPTS_DIR)/format_go_fieldalignment.sh

.PHONY: format_go_tag_alignment
format_go_tag_alignment:
	@$(SCRIPTS_DIR)/format_go_tag_alignment.sh

.PHONY: go_fix
go_fix:
	go fix ./...

.PHONY: goimports
goimports:
	$(SCRIPTS_DIR)/goimports.sh $(PWD)

.PHONY: format_golang
format_golang: go_fix goimports format_imports format_go_fieldalignment format_go_tag_alignment
	@$(SCRIPTS_DIR)/format_golang.sh $(PWD)

.PHONY: format
format: format_golang

.PHONY: fmt
fmt: format

## LINTING

.PHONY: golang_lint
golang_lint:
	@mkdir -p $(GO_CACHE)/build $(GO_CACHE)/mod
	@$(SCRIPTS_DIR)/golang_lint.sh $(CONTAINER_RUNNER) $(LINTER_IMAGE) "$(LINTER)"

.PHONY: shellcheck
shellcheck:
	@$(SCRIPTS_DIR)/shellcheck.sh $(CONTAINER_RUNNER) $(SHELLCHECK_IMAGE) $(SCRIPTS_DIR)

.PHONY: sqlc_compile
sqlc_compile:
	@$(SCRIPTS_DIR)/sqlc_compile.sh $(PWD)

# Deliberately not a prerequisite of `generate`, for the reason proto is not:
# the unison toolchain is a pinned download that almost no `go generate` run
# needs, and the generated-files workflow diffs its output separately.
.PHONY: unison
unison:
	@$(SCRIPTS_DIR)/unison_generate.sh $(PWD)

.PHONY: lint
lint: golang_lint shellcheck

## GENERATION

.PHONY: generate
generate:
	go generate ./...

# Deliberately not a prerequisite of `generate`. protoc is a pinned download
# rather than a module dependency, and `go generate ./...` is run far more often
# than a .proto changes; folding one into the other would make every generate
# reach for a toolchain almost none of them need. CI runs both, in separate jobs.
#
# Formatting is the caller's, not the script's: `make proto format` is what a
# contributor runs and what CI checks, so the generated files are held to the
# same formatters as everything else.
.PHONY: proto
proto:
	$(SCRIPTS_DIR)/proto.sh $(PWD)

## EXECUTION

.PHONY: build
build:
	$(SCRIPTS_DIR)/build.sh $(TOTAL_PACKAGE_LIST)

.PHONY: test
test: $(ARTIFACTS_DIR)
	$(SCRIPTS_DIR)/test.sh

.PHONY: bench
bench:
	$(SCRIPTS_DIR)/benchmark.sh
