# Architecture

## Component Overview

```mermaid
graph TB
    subgraph "shed microVM"
        SSH[SSH client / git] --> SSA[shed-ssh-agent<br/>Unix socket]
        SSA --> BUS[shed-agent<br/>127.0.0.1:498]
    end
    BUS -->|vsock port 1026| SRV[shed-server<br/>Plugin message bus]
    SRV -->|SSE| HA[shed-host-agent]
    subgraph "Host macOS"
        HA --> BE[SSH Backend<br/>agent-forward / local-keys]
        HA --> TID[Touch ID gate]
        HA --> AL[Audit log]
    end
```

## Message Flow

### SSH Sign Request

1. SSH client connects to `shed-ssh-agent` via `SSH_AUTH_SOCK`
2. `shed-ssh-agent` translates the SSH agent protocol `Sign()` call into a JSON envelope
3. Envelope is POSTed to `http://127.0.0.1:498/v1/publish` (shed-agent's HTTP endpoint)
4. shed-agent sends the message over vsock to shed-server
5. shed-server routes the message to the `ssh-agent` namespace listener via SSE
6. `shed-host-agent` receives the envelope, dispatches to the SSH backend
7. SSH backend performs the signing operation using host keys
8. Response envelope flows back: host-agent -> shed-server -> shed-agent -> shed-ssh-agent
9. `shed-ssh-agent` returns the signature to the SSH client

## Package Structure

### Guest-Side

- **`internal/sshagent/`** — Implements `golang.org/x/crypto/ssh/agent.Agent`. Each method marshals a request, POSTs to the publish endpoint, and unmarshals the response.
- **`cmd/shed-ssh-agent/`** — Unix socket listener. Creates a new agent instance per connection. Handles startup health check and graceful shutdown.

### Host-Side

- **`internal/hostclient/`** — SSE client for shed-server's plugin API. Handles subscription, reconnection, and response delivery.
- **`cmd/shed-host-agent/`** — Main binary. Loads config, auto-detects SSH backend, subscribes to namespaces, dispatches requests to handlers.

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

## Security Boundaries

| Boundary | What crosses | What doesn't |
|----------|-------------|--------------|
| VM -> Host (sign request) | Challenge data, public key reference | Private keys |
| Host -> VM (sign response) | Signature blob | Private keys, other credentials |
| VM -> Host (list request) | Nothing | - |
| Host -> VM (list response) | Public keys | Private keys |
