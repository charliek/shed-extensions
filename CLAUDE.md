# CLAUDE.md

Project context for AI assistants working on this codebase.

## Project Overview

shed-extensions provides secure credential brokering between shed microVMs and the developer's host machine. Credentials never enter the VM — all signing and secret resolution happens on the host, mediated by shed's plugin message bus.

## Build & Test

```bash
make build          # Build all binaries (host + guest)
make build-host     # Build shed-host-agent (macOS, CGO for Touch ID)
make build-guest    # Cross-compile guest binaries for Linux (amd64 + arm64)
make test           # Run all unit tests
make lint           # Run golangci-lint
make fmt            # Format code with gofmt
make check          # Run lint + test
make coverage       # Tests with coverage report
```

Tools are managed via [mise](https://mise.jdx.dev/) — run `mise install` to set up Go and golangci-lint.

## Project Structure

- `cmd/shed-host-agent/` — Host-side daemon (macOS): subscribes to shed-server plugin bus, handles SSH and AWS credential operations
- `cmd/shed-ssh-agent/` — Guest-side SSH agent adapter (Linux): translates SSH agent protocol to message bus requests
- `cmd/shed-aws-proxy/` — Guest-side AWS credential proxy (Linux): serves AWS container credential endpoint, translates to message bus requests
- `cmd/shed-ext/` — Guest-side status CLI (Linux): in-VM health check for namespace connectivity
- `internal/protocol/` — Shared envelope and payload types (JSON wire format matches shed's plugin types)
- `internal/busclient/` — Shared guest-side publish-to-bus client (used by all guest binaries)
- `internal/sshagent/` — SSH agent.Agent implementation that publishes to the message bus
- `internal/awsproxy/` — AWS credential HTTP endpoint (passthrough to bus)
- `internal/hostclient/` — SSE client for shed-server's plugin API
- `internal/version/` — Build-time version information
- `image/` — Base image overlay files (systemd units, environment config)
- `docs/` — MkDocs documentation site

## Key Conventions

- **Go version**: 1.24+ (see `go.mod`)
- **Formatting**: `gofmt` — run `make fmt` before committing
- **Linting**: `golangci-lint` — run `make lint`
- **Tests**: Table-driven tests with `t.Run()`. Place `_test.go` files alongside source.
- **Build tags**: `darwin` for Touch ID code, `!darwin` for stubs
- **Guest binaries**: Pure Go (`CGO_ENABLED=0`), cross-compiled for `linux/amd64` and `linux/arm64`
- **Host binary**: Requires CGO on macOS for Touch ID (`LocalAuthentication` framework)
- **Protocol types**: Defined locally in `internal/protocol/` — JSON wire format matches `github.com/charliek/shed/internal/plugin` without importing it

## Plugin Integration

This project hooks into shed-server's plugin message bus:
- Guest binaries POST to `http://127.0.0.1:498/v1/publish` (shed-agent's HTTP endpoint)
- Host binary subscribes via SSE at `GET /api/plugins/listeners/{namespace}/messages`
- Host binary responds via `POST /api/plugins/listeners/{namespace}/respond`
- Namespaces: `ssh-agent`, `aws-credentials`

## Documentation

Docs use MkDocs Material. See `mkdocs.yml` for style guidelines (top comment block).

```bash
make docs-serve     # Serve docs at http://127.0.0.1:7071
```
