.PHONY: build build-host build-guest test lint fmt tidy check clean coverage docs docs-serve

GOARCH ?= $(shell go env GOARCH)

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X github.com/charliek/shed-extensions/internal/version.Version=$(VERSION) -X github.com/charliek/shed-extensions/internal/version.GitCommit=$(GIT_COMMIT) -X github.com/charliek/shed-extensions/internal/version.BuildDate=$(BUILD_DATE)"

# Build all binaries
build: build-host build-guest

# Build host agent (macOS, needs CGO for Touch ID)
build-host:
	go build $(LDFLAGS) -o bin/shed-host-agent ./cmd/shed-host-agent

# Build guest binaries (Linux, pure Go)
build-guest:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/linux-arm64/shed-ssh-agent ./cmd/shed-ssh-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/linux-amd64/shed-ssh-agent ./cmd/shed-ssh-agent

# Run all unit tests
test:
	go test -v ./...

# Run linter
lint:
	golangci-lint run

# Format code
fmt:
	go fmt ./...

# Tidy dependencies
tidy:
	go mod tidy

# Run all checks (lint + test)
check: lint test

# Clean build artifacts
clean:
	rm -rf bin/ dist/

# Run tests with coverage
coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Build documentation
docs:
	uv sync --group docs
	uv run mkdocs build

# Serve documentation locally
docs-serve:
	uv sync --group docs
	uv run mkdocs serve
