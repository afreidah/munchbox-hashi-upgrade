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

generate: ## Generate interface mocks
	go generate ./...
	@$(MAKE) --no-print-directory strip-mock-package-docs

# mockgen writes "Package X is a generated GoMock package." above every package
# clause. In a package whose mocks are not _test.go files that comment is a real
# package comment, and go/doc concatenates it onto the one in interface.go.
strip-mock-package-docs:
	@for f in $$(git ls-files --cached --others --exclude-standard '*.go' | grep -v '_test\.go$$' | xargs -r grep -l 'is a generated GoMock package\.'); do \
		perl -0pi -e 's{\n// Package \w+ is a generated GoMock package\.\n(package )}{\n$$1}' $$f; \
		echo "  stripped mockgen package comment: $$f"; \
	done

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

# The integration tier runs Nomad in containers, so it needs Docker and a few
# minutes rather than a few seconds. Its profile is written separately and
# merged by the coverage dashboard: a line is covered whichever tier reached
# it, and much of the client packages is only reachable against a real cluster.
INTEGRATION_COVER_FLAGS := -race -v -tags integration -count=1 -timeout 15m \
	-coverprofile=integration-coverage.out -covermode=atomic -coverpkg=./...

integration-test: ## Run the integration tests (requires Docker)
	go test -race -v -tags integration -count=1 -timeout 15m ./internal/integration/

integration-coverage: ## Generate integration-coverage.out (requires Docker)
	go test $(INTEGRATION_COVER_FLAGS) ./internal/integration/
	@go tool cover -func=integration-coverage.out | tail -1

##@ Test cluster

COMPOSE := docker compose -f docker-compose.test.yml
CLUSTER_ADDR := http://127.0.0.1:4646

# The version the fleet starts on, behind whatever you then plan toward. A
# cluster already at the target plans a run whose every task is a no-op.
FROM_VERSION ?= 2.0.5

KEYS := test/node/keys

cluster-keys: $(KEYS)/user_key ## Generate the throwaway ssh material the test fleet uses

# The tool will not accept a bare host key -- it verifies the host certificate
# against a CA -- so a test fleet needs a CA of its own. One host key across
# the fleet, and a certificate with no principals, which ssh treats as valid
# for any hostname: the containers' addresses are assigned at start and are not
# worth signing for individually.
$(KEYS)/user_key:
	@mkdir -p $(KEYS)
	# Cleared first: the recipe writes four keys but the target tracks one, so
	# a partial set left behind has ssh-keygen stop to ask about overwriting.
	@rm -f $(KEYS)/host_ca* $(KEYS)/host_key* $(KEYS)/user_key* $(KEYS)/cinc_key*
	ssh-keygen -q -t ed25519 -N '' -C host-ca -f $(KEYS)/host_ca
	ssh-keygen -q -t ed25519 -N '' -C test-node -f $(KEYS)/host_key
	ssh-keygen -q -s $(KEYS)/host_ca -I test-fleet -h $(KEYS)/host_key.pub
	ssh-keygen -q -t ed25519 -N '' -C hashi-upgrade -f $(KEYS)/user_key
	# The configuration server in this fleet verifies nothing, so what the tool
	# signs with only has to parse. RSA because that is what the Chef scheme
	# uses, and generated here rather than extracted from the server: writing
	# its admin key into a mounted directory fails under a remapped user
	# namespace, and an unverified signature makes the real key pointless.
	ssh-keygen -q -t rsa -b 2048 -m PEM -N '' -C hashi-upgrade -f $(KEYS)/cinc_key
	@printf '\nssh material written to %s\n' '$(KEYS)'

cluster-up: cluster-keys ## Start a local Nomad fleet (3 servers, 2 clients) on FROM_VERSION
	# --remove-orphans: a service renamed between versions of this file leaves a
	# container compose no longer knows about, still holding the published port.
	FROM_VERSION=$(FROM_VERSION) $(COMPOSE) up -d --build --wait --remove-orphans
	@printf '\nfleet is on %s\nexport NOMAD_ADDR=%s\n\n' '$(FROM_VERSION)' '$(CLUSTER_ADDR)'
	@NOMAD_ADDR=$(CLUSTER_ADDR) nomad server members 2>/dev/null || true
	@printf '\nnext: make cluster-plan && make cluster-run\n\n'

cluster-down: ## Stop the local fleet and discard its state
	FROM_VERSION=$(FROM_VERSION) $(COMPOSE) down -v --remove-orphans

cluster-logs: ## Follow the local fleet's logs
	FROM_VERSION=$(FROM_VERSION) $(COMPOSE) logs -f

# Everything a run needs to reach the local fleet. The credentials are the
# throwaway ones cluster-keys generates; the configuration server verifies no
# signature, so the key it signs with only has to parse.
CLUSTER_FLAGS := \
	--cinc-server http://127.0.0.1:8889/organizations/test \
	--cinc-client pivotal --cinc-key $(KEYS)/cinc_key \
	--ssh-key $(KEYS)/user_key --ssh-host-ca $(KEYS)/host_ca.pub

# The version to plan toward, ahead of FROM_VERSION so there is work to do.
TO ?= 2.0.7

# The newest run file in the working directory, which is the one plan just
# wrote. Override to drive an older one.
RUN_FILE ?= $(shell ls -t nomad-*.yaml 2>/dev/null | head -1)

cluster-plan: build ## Survey the local fleet and write a run file (TO=<version>)
	NOMAD_ADDR=$(CLUSTER_ADDR) ./$(BINARY) plan nomad --to $(TO)

cluster-run: build ## Drive the newest run file against the local fleet (ARGS=--yes)
	@test -n "$(RUN_FILE)" || { echo "no run file; run 'make cluster-plan' first"; exit 1; }
	@printf 'driving %s\n\n' '$(RUN_FILE)'
	NOMAD_ADDR=$(CLUSTER_ADDR) ./$(BINARY) run $(RUN_FILE) $(CLUSTER_FLAGS) $(ARGS)

##@ Housekeeping

clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out coverage.html integration-coverage.out

.PHONY: help build install uninstall generate strip-mock-package-docs test test-fast vet lint fmt govulncheck check coverage integration-test integration-coverage cluster-keys cluster-up cluster-down cluster-logs cluster-plan cluster-run clean
.DEFAULT_GOAL := help
