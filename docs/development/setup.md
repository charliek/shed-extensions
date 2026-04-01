# Development Setup

## Prerequisites

- Go 1.24+
- golangci-lint 2.10.1+
- [mise](https://mise.jdx.dev/) (recommended for tool management)

## Getting Started

```bash
git clone https://github.com/charliek/shed-extensions.git
cd shed-extensions
mise install        # installs Go and golangci-lint
make check          # runs lint + tests
```

## Building

```bash
make build          # build all binaries
make build-host     # build shed-host-agent (macOS)
make build-guest    # cross-compile guest binaries (Linux amd64 + arm64)
```

## Testing

```bash
make test           # run all unit tests
make coverage       # tests with HTML coverage report
```

## Linting

```bash
make lint           # run golangci-lint
make fmt            # format code with gofmt
make check          # lint + test together
```

## Documentation

Docs use [MkDocs Material](https://squidfunk.github.io/mkdocs-material/). Requires [uv](https://docs.astral.sh/uv/) for Python dependency management.

```bash
make docs-serve     # serve at http://127.0.0.1:7071
make docs           # build to site-build/
```

## Manual Testing

For end-to-end testing against a running shed:

1. Start shed-server: `shed-server serve`
2. Start host agent: `go run ./cmd/shed-host-agent --config ~/.config/shed/extensions.yaml`
3. Create a shed with the extensions-enabled image
4. Inside the shed: `ssh -T git@github.com`
