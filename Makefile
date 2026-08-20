GO      ?= go
NPM     ?= npm
BIN     := bin/fastr
PKG     := ./...
WEB     := web
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'

# --- build --------------------------------------------------------------------

.PHONY: web
web: ## Build the web application into internal/assets/dist
	cd $(WEB) && $(NPM) ci --no-audit --no-fund && $(NPM) run build
	@# Vite empties the output directory, which would remove the placeholder
	@# that lets `go build` succeed on a fresh clone. Put it back.
	@touch internal/assets/dist/.gitkeep

.PHONY: build
build: web ## Build the binary for the host platform
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/fastr

.PHONY: build-all
build-all: web ## Cross-compile Linux and Windows, both architectures
	@for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags '$(LDFLAGS)' -o bin/fastr-$$os-$$arch$$ext ./cmd/fastr || exit 1; \
	done

# --- quality gates ------------------------------------------------------------
# Constitution v2.0.1 makes all six mandatory before merge.

.PHONY: test
test: ## Unit and integration tests
	CGO_ENABLED=0 $(GO) test -count=1 $(PKG)

.PHONY: test-e2e
test-e2e: ## Browser tests against Chromium and WebKit
	cd $(WEB) && $(NPM) run test:e2e

.PHONY: test-a11y
test-a11y: ## Accessibility gate: axe-core plus keyboard-only traversal
	cd $(WEB) && $(NPM) run test:a11y

.PHONY: test-security
test-security: ## Unpaired access, path traversal, replay, log hygiene
	CGO_ENABLED=0 $(GO) test -count=1 -run 'Security|Traversal|Replay|Unpaired|LogHygiene' $(PKG)

.PHONY: test-network
test-network: ## Assert zero sockets leave the local network
	CGO_ENABLED=0 $(GO) test -count=1 -run 'NetworkBoundary' $(PKG)

.PHONY: test-large
test-large: ## Memory flatness across a 10 GB transfer. Slow; nightly in CI.
	CGO_ENABLED=0 $(GO) test -count=1 -timeout 60m -tags large -run 'LargeTransfer' $(PKG)

.PHONY: lint
lint: ## Lint Go and the web application
	golangci-lint run
	cd $(WEB) && $(NPM) run lint

.PHONY: fmt
fmt: ## Format everything
	$(GO) fmt $(PKG)
	cd $(WEB) && $(NPM) run format

# --- development helpers ------------------------------------------------------

SIZE ?= 1G

.PHONY: fixture
fixture: ## Generate a large test fixture. Usage: make fixture SIZE=10G
	$(GO) run ./test/testdata/generate.go -size $(SIZE) -out test/testdata/large

.PHONY: capture
capture: ## Capture a transfer and assert the payload does not appear in the clear
	$(GO) run ./test/tools/capture

PID ?= 0

.PHONY: watch-memory
watch-memory: ## Watch resident memory of a running instance. Usage: make watch-memory PID=1234
	@test $(PID) -ne 0 || (echo "set PID=<pid>"; exit 1)
	@while kill -0 $(PID) 2>/dev/null; do \
		grep VmRSS /proc/$(PID)/status; sleep 2; \
	done

DURATION ?= 30s

.PHONY: netcut
netcut: ## Block the service port for a while, to exercise resume
	$(GO) run ./test/tools/netcut -duration $(DURATION)

.PHONY: clean
clean: ## Remove build output
	rm -rf bin
	rm -rf internal/assets/dist
	mkdir -p internal/assets/dist && touch internal/assets/dist/.gitkeep
