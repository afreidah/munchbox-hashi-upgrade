# -------------------------------------------------------------------------------
# Munchbox HashiCorp Upgrader - Build, Lint, Test
#
# Author: Alex Freidah
#
# CLI that rolls Nomad, Consul and Vault upgrades across a cluster: discovers
# topology, generates a gated plan, and drives cinc-client node by node.
# -------------------------------------------------------------------------------

BINARY  := hashi-upgrade
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Install location for `make install` (override e.g. BINDIR=$(HOME)/.local/bin
# for a no-sudo user install).
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin

GO_LDFLAGS := -s -w -X github.com/afreidah/munchbox-hashi-upgrade/internal/cli.Version=$(VERSION)

# Keep in sync with the version pinned in .github/workflows/ci.yml.
GOLANGCI_VERSION ?= v2.13.0
GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

# Mock packages hold no tests, so each adds a 0.0% row to the report and drags
# the repo total down by its own generated statements. Matched by shape rather
# than by name, so the next <pkg>mock is excluded without anyone editing this.
TEST_PKGS ?= $$(go list ./... | grep -vE '/[a-z]+mock$$')

# -------------------------------------------------------------------------
# DEFAULT TARGET
# -------------------------------------------------------------------------

help: ## Display available Make targets
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage: make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z0-9_-]+:.*?## / { \
			gsub(/[A-Z_][A-Z0-9_]*=[a-zA-Z0-9_|-]+/, "\033[33m&\033[0m", $$2); \
			printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 \
		} \
		/^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5)}' $(MAKEFILE_LIST)

##@ Build

build: ## Build the binary for the local platform
	go build -ldflags="$(GO_LDFLAGS)" -o $(BINARY) ./cmd/hashi-upgrade

install: build ## Install the binary to BINDIR (default /usr/local/bin; needs sudo, or set BINDIR=$(HOME)/.local/bin)
	install -d $(BINDIR)
	install -m 0755 $(BINARY) $(BINDIR)/$(BINARY)

uninstall: ## Remove the installed binary from BINDIR
	rm -f $(BINDIR)/$(BINARY)

##@ Quality

test: ## Run Go tests with race detection and coverage
	go test -race -cover $(TEST_PKGS)

test-fast: ## Run Go tests without race or coverage for quick iteration
	go test $(TEST_PKGS)

vet: ## Run Go vet static analysis
	go vet ./...

lint: ## Run Go linter
	$(GOLANGCI) run ./...

fmt: ## Apply the formatting lint enforces (gofmt + goimports)
	$(GOLANGCI) fmt ./...

govulncheck: ## Scan Go dependencies for known vulnerabilities
	go tool govulncheck ./...

check: ## Run fast local checks for contributor iteration
	$(MAKE) test-fast
	$(MAKE) vet

# Flags mirror .github/workflows/ci.yml so the local profile matches CI.
# -covermode=atomic is required for race-enabled runs.
COVER_FLAGS := -race -coverprofile=coverage.out -covermode=atomic -coverpkg=./...

coverage: ## Generate coverage.out from unit tests (mirrors the CI test job)
	go test $(COVER_FLAGS) $(TEST_PKGS)
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1

##@ Housekeeping

clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out coverage.html

.PHONY: help build install uninstall test test-fast vet lint fmt govulncheck check coverage clean
.DEFAULT_GOAL := help
