# Architecture

## Component Overview

```mermaid
graph TB
    subgraph "shed microVM"
        SSH[SSH client / git] --> SSA[shed-ext-ssh-agent<br/>Unix socket]
        AWS[AWS SDK] --> AWP[shed-ext-aws-credentials<br/>HTTP :499]
        DCK[Docker CLI] --> DCS[docker-credential-shed<br/>one-shot CLI]
        SSA --> BUS[shed-agent<br/>127.0.0.1:498]
        AWP --> BUS
        DCS --> BUS
    end
    BUS -->|vsock port 1026| SRV[shed-server<br/>Plugin message bus]
    SRV -->|SSE| HA[shed-host-agent]
    subgraph "Host macOS"
        HA --> BE[SSH Backend<br/>agent-forward / local-keys]
        HA --> STS[AWS STS<br/>AssumeRole + cache]
        HA --> DCB[Docker Backend<br/>credential helpers + config]
        HA --> TID[Touch ID gate]
        HA --> AL[Audit log]
    end
```

## Message Flow

### SSH Sign Request

1. SSH client connects to `shed-ext-ssh-agent` via `SSH_AUTH_SOCK`
2. `shed-ext-ssh-agent` translates the SSH agent protocol `Sign()` call into a JSON envelope
3. Envelope is POSTed to `http://127.0.0.1:498/v1/publish` (shed-agent's HTTP endpoint)
4. shed-agent sends the message over vsock to shed-server
5. shed-server routes the message to the `ssh-agent` namespace listener via SSE
6. `shed-host-agent` receives the envelope, dispatches to the SSH backend
7. SSH backend performs the signing operation using host keys
8. Response envelope flows back: host-agent -> shed-server -> shed-agent -> shed-ext-ssh-agent
9. `shed-ext-ssh-agent` returns the signature to the SSH client

### AWS Credential Request

1. AWS SDK calls `GET http://127.0.0.1:499/credentials` (via `AWS_CONTAINER_CREDENTIALS_FULL_URI`)
2. `shed-ext-aws-credentials` translates the HTTP request into a JSON envelope
3. Envelope is POSTed to the shed-agent publish endpoint
4. shed-server routes the message to the `aws-credentials` namespace listener via SSE
5. `shed-host-agent` receives the envelope, checks its credential cache
6. If cached credentials are still valid (>5 min remaining), return immediately
7. If stale, call `sts:AssumeRole` with the configured role, cache result
8. Response flows back to `shed-ext-aws-credentials`, which returns the AWS SDK-expected format
9. The SDK caches the credential in memory and manages its own refresh

```mermaid
sequenceDiagram
    participant SDK as AWS SDK
    participant Proxy as shed-ext-aws-credentials
    participant Bus as Message Bus
    participant Host as shed-host-agent
    participant STS as AWS STS

    SDK->>Proxy: GET /credentials
    Proxy->>Bus: publish(aws-credentials)
    Bus->>Host: SSE event
    alt Cache hit
        Host-->>Bus: cached credentials
    else Cache miss
        Host->>STS: AssumeRole
        STS-->>Host: temp credentials
        Host-->>Bus: fresh credentials
    end
    Bus-->>Proxy: response
    Proxy-->>SDK: {AccessKeyId, SecretAccessKey, Token, Expiration}
```

### Docker Credential Request

1. Docker CLI execs `docker-credential-shed get` with the registry hostname on stdin
2. `docker-credential-shed` translates the request into a JSON envelope and POSTs to the shed-agent publish endpoint
3. shed-server routes the message to the `docker-credentials` namespace listener via SSE
4. `shed-host-agent` checks the registry allowlist, reads `~/.docker/config.json`, and shells out to the appropriate credential helper (gcloud, osxkeychain, ecr-login, etc.)
5. Response flows back to `docker-credential-shed`, which writes credentials to stdout and exits

## Package Structure

### Guest-Side

- **`internal/sshagent/`** — Implements `golang.org/x/crypto/ssh/agent.Agent`. Each method marshals a request, POSTs to the publish endpoint, and unmarshals the response.
- **`internal/awsproxy/`** — HTTP handler for the AWS container credential endpoint. Translates `GET /credentials` into message bus requests. Returns the PascalCase JSON format the AWS SDK expects.
- **`internal/dockercred/`** — Docker credential helper bus client. Translates Docker credential helper protocol operations (`get`, `list`) into message bus requests. One-shot usage (not a daemon).
- **`cmd/shed-ext-ssh-agent/`** — Unix socket listener. Creates a new agent instance per connection. Handles startup health check and graceful shutdown.
- **`cmd/shed-ext-aws-credentials/`** — HTTP server on port 499. Routes `/credentials` to the proxy handler.
- **`cmd/docker-credential-shed/`** — One-shot CLI binary. Docker execs this binary per credential operation. Reads stdin, publishes to bus, writes stdout, exits.

### Host-Side

- **`internal/hostclient/`** — SSE client for shed-server's plugin API. Handles subscription, reconnection, and response delivery.
- **`cmd/shed-host-agent/`** — Main binary. Loads config, initializes backends, subscribes to namespaces, dispatches requests to handlers. Runs SSH, AWS, and Docker handlers concurrently.

### Shared

- **`internal/protocol/`** — Envelope and payload types. Defined locally (not imported from shed) to avoid dependency coupling. JSON wire format matches shed's `internal/plugin` types.

## SSH Backend Selection

```mermaid
graph TD
    A[Start] --> B{config.ssh.mode set?}
    B -->|Yes| C[Use configured mode]
    B -->|No| D{SSH_AUTH_SOCK exists?}
    D -->|Yes| E[agent-forward mode]
    D -->|No| F[local-keys mode]
```

**Agent-forward**: Proxies to the developer's existing SSH agent (Secretive, 1Password, ssh-agent, yubikey-agent). Zero disruption to existing key management.

**Local-keys**: Reads keys directly from `~/.ssh/`. Fallback when no agent is running.

## AWS Credential Caching

The caching strategy is asymmetric:

- **Guest proxy**: Pure passthrough, no caching. Every SDK request goes to the bus.
- **Host handler**: Caches STS credentials per shed, keyed by shed name.

This avoids cache coherence complexity. The bus round trip is sub-millisecond (vsock, same machine), and the SDK only fetches credentials when its in-memory cache is stale (~once per hour).

## Distribution

Artifacts are split across two channels:

| Component | Channel | Trigger |
|-----------|---------|---------|
| `shed-host-agent` (darwin + linux, arm64 + amd64) | GitHub Release (GoReleaser) | git tag |
| Guest binaries + systemd units + env config | Multi-arch Docker image (`ghcr.io/charliek/shed-extensions:<tag>`) | git tag |

The Docker image is a `scratch`-based container with just the binaries and config files. It's consumed by shed's VZ and Firecracker Dockerfiles via `COPY --from=ghcr.io/charliek/shed-extensions:<version>`. Shed pins the version with an `ARG SHED_EXT_VERSION` in each Dockerfile.

Both are published by the same release workflow (`.github/workflows/release.yaml`) from the same git tag.

## Security Boundaries

| Boundary | What crosses | What doesn't |
|----------|-------------|--------------|
| VM -> Host (SSH sign) | Challenge data, public key reference | Private keys |
| Host -> VM (SSH response) | Signature blob | Private keys |
| VM -> Host (AWS request) | Operation type only | Role ARN, source credentials |
| Host -> VM (AWS response) | Short-lived STS token (1h) | Long-lived AWS credentials |
| VM -> Host (Docker get) | Registry hostname | Docker config.json contents |
| Host -> VM (Docker response) | Registry credentials (tokens or passwords) | Host Docker config, other registries' credentials |
