# Changelog

## v0.3.1

### Features

- Add Docker registry credential helper extension (`docker-credentials` namespace)
- Guest binary `docker-credential-shed` brokers Docker registry credentials from host to VM
- Host backend reads `~/.docker/config.json` and shells out to credential helpers (gcloud, osxkeychain, ecr-login, etc.)
- Configurable registry allowlist with `allow_all` option
- Read-only broker — store and erase operations are rejected
- Audit logging for all Docker credential operations

## v0.2.0

### Features

- SSH agent credential brokering — proxies SSH sign operations from guest VMs to host keys
- AWS credential brokering — vends short-lived STS credentials to guest VMs via container credential endpoint
- Guest-side status CLI (`shed-ext status`) for checking namespace connectivity
- Touch ID approval gate for SSH sign operations (macOS, opt-in)
- Host-side audit logging of all credential operations
- Docker image distribution for guest binaries (`ghcr.io/charliek/shed-extensions`)

### Fixes

- Run guest services as `shed` user to fix socket permissions
- Fix stale socket paths in documentation

### Infrastructure

- Multi-arch Docker image (linux/arm64 + linux/amd64) for guest binary distribution
- GoReleaser config for `shed-host-agent` (darwin + linux)
- CI release workflow publishes both GitHub Release and Docker image on git tag
- Prerelease-safe `:latest` tagging for Docker images
- MkDocs Material documentation site
