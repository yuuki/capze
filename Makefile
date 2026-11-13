BIN     := capze
PKG     := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Run privileged targets with sudo unless already root (e.g. in a container
# without sudo).
SUDO := $(if $(filter 0,$(shell id -u)),,sudo)

# Tool versions. The workflows (.github/workflows) pin the same versions:
# bump them together.
GOLANGCI_LINT_VERSION   ?= v2.15.0
GORELEASER_VERSION      ?= v2.20.0
GOVULNCHECK_VERSION     ?= v1.9.0
ACTIONLINT_VERSION      ?= v1.7.12
ZIZMOR_VERSION          ?= 1.32.0
GH_ACTIONS_LOCK_VERSION ?= v0.4.1

FUZZTIME ?= 30s

# golangci-lint and govulncheck type-check with the go/types they were built
# with, and `go run tool@version` builds a tool with the Go of the tool's own
# go.mod: build them with the Go of this module, or a newer one if the tool
# needs it (+auto).
GO_TOOLCHAIN  = $(shell go env GOVERSION)
GOLANGCI_LINT = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
GORELEASER    = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
ACTIONLINT    = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
# uvx, or pipx (preinstalled on GitHub-hosted runners), runs the pinned zizmor.
ZIZMOR        = $(if $(shell command -v uvx 2>/dev/null),uvx zizmor@$(ZIZMOR_VERSION),pipx run zizmor==$(ZIZMOR_VERSION))
# gh-actions-lock (the `gh actions-lock` extension) runs through `go run` too.
GH_ACTIONS_LOCK = GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto go run github.com/github/gh-actions-lock/cmd/gh-actions-lock@$(GH_ACTIONS_LOCK_VERSION)
# Extra zizmor options, e.g. --format=github for annotations in CI.
ZIZMOR_FLAGS ?=

.DEFAULT_GOAL := build

# Phony on purpose: go build is incremental, and a file target without
# prerequisites would never rebuild a stale binary.
.PHONY: build
build: ## Build a static capze binary
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

.PHONY: check
check: lint lint-actions vet test-race build ## Pre-PR gate: lint, workflow audit, vet, race tests and build (no root needed)

.PHONY: test
test: ## Run the tests (those that mount file systems skip without root)
	go test $(PKG)

.PHONY: test-race
test-race: ## Run the tests with the race detector, as CI does
	go test -race $(PKG)

# The tests that bind-mount directories of their own temporary directory, to
# check that nothing is deleted through a mount point. They skip where
# mounting is not allowed (an unprivileged container).
.PHONY: test-root
test-root: ## Run the tests as root, including the ones that mount file systems
	$(SUDO) env "PATH=$$PATH" "GOTOOLCHAIN=$$(go env GOTOOLCHAIN)" "GOFLAGS=$$(go env GOFLAGS)" go test -race -count=1 $(PKG)

# go test -list runs on its own so that a package that does not build fails
# the target instead of silently having no fuzz targets. A failing input is
# written to testdata/fuzz/<target>/ of its package.
.PHONY: test-fuzz
test-fuzz: ## Run every fuzz target for FUZZTIME (default 30s)
	@set -e; for pkg in $$(go list $(PKG)); do \
		targets=$$(go test -list '^Fuzz' $$pkg); \
		for f in $$(printf '%s\n' "$$targets" | grep '^Fuzz' || true); do \
			echo "== $$pkg $$f"; \
			go test -run='^$$' -fuzz="^$$f\$$" -fuzztime=$(FUZZTIME) -fuzzminimizetime=5s $(FUZZFLAGS) $$pkg; \
		done; \
	done

.PHONY: cover
cover: ## Write a coverage report to coverage.out
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet for every released platform
	go vet $(PKG)
	GOOS=darwin go vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint and check that go.mod and go.sum are tidy
	$(GOLANGCI_LINT) run
	go mod tidy -diff

# Without a GitHub token (GH_TOKEN), zizmor runs offline; CI gives it one for
# the audits that ask the GitHub API (impostor commits, known vulnerable
# actions, ...).
# gh-actions-lock checks that .github/workflows/actions.lock pins every
# action the workflows use; with GH_TOKEN it also verifies each pin upstream.
.PHONY: lint-actions
lint-actions: ## Lint (actionlint) and audit (zizmor) the GitHub Actions workflows, and check their lockfile
	$(ACTIONLINT)
	$(ZIZMOR) $(ZIZMOR_FLAGS) $(if $(GH_TOKEN),,--offline) .
	$(GH_ACTIONS_LOCK) $(if $(GH_TOKEN),--verify,--verify-local)

# Pin the actions in .github/workflows/actions.lock after a `uses:` changed,
# then review the diff. Needs a GitHub token (GH_TOKEN or `gh auth login`).
.PHONY: lock-actions
lock-actions: ## Update .github/workflows/actions.lock after changing a uses: line
	$(GH_ACTIONS_LOCK)

.PHONY: fmt
fmt: ## Format the code
	$(GOLANGCI_LINT) fmt

.PHONY: vuln
vuln: ## Report known vulnerabilities reachable from capze (govulncheck)
	$(GOVULNCHECK) $(PKG)

.PHONY: cross
cross: ## Check that capze builds for every released platform
	for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		GOOS=$${p%/*} GOARCH=$${p#*/} CGO_ENABLED=0 go build -o /dev/null . || exit 1; \
	done

.PHONY: snapshot
snapshot: ## Build the release assets into dist/ without publishing them
	$(GORELEASER) check
	$(GORELEASER) release --snapshot --clean

.PHONY: clean
clean: ## Remove build outputs
	rm -f $(BIN) coverage.out
	rm -rf dist

.PHONY: help
help: ## Show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'
