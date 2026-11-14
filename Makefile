BINARY     := astro-liomix-server
BUILD_DIR  := bin
PKG        := ./...
GO         ?= go
EXE        := $(if $(filter Windows_NT,$(OS)),.exe,)
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildTime=$(BUILD_TIME)

.PHONY: help run build test test-race cover vet lint fmt tidy clean

help: ## Show the available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

run: ## Run the API on HTTP_ADDR (default :8080)
	$(GO) run ./cmd/server

build: ## Compile the server into bin/
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)$(EXE) ./cmd/server

test: ## Run the test suite
	$(GO) test -count=1 $(PKG)

test-race: ## Run the test suite under the race detector (needs cgo on Windows)
	$(GO) test -race -count=1 $(PKG)

cover: ## Write coverage.out and coverage.html
	$(GO) test -count=1 -covermode=atomic -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out | tail -1
	$(GO) tool cover -html=coverage.out -o coverage.html

vet: ## Run go vet
	$(GO) vet $(PKG)

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format the tree
	gofmt -l -w .

tidy: ## Sync go.mod and go.sum
	$(GO) mod tidy

clean: ## Remove build and coverage artefacts
	rm -rf $(BUILD_DIR) coverage.out coverage.html
