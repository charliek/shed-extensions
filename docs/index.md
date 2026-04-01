# shed-extensions

Secure credential brokering for shed microVM development environments.

## What it does

shed-extensions keeps credentials off your VMs. SSH keys never leave your Mac. AWS secrets never enter the guest. All signing and credential resolution happens on the host, mediated by shed's plugin message bus.

Standard tools work without changes — `git push`, AWS SDKs, `ssh` — all transparently proxied through the credential broker.

## Architecture

```mermaid
graph LR
    subgraph "shed microVM (Linux guest)"
        A[SSH client / git] --> B[shed-ssh-agent]
        B --> C[shed-agent bus<br/>127.0.0.1:498]
    end
    C -->|vsock| D[shed-server]
    D -->|SSE| E[shed-host-agent]
    subgraph "Host (macOS)"
        E --> F[SSH keys / agent]
        E --> G[Touch ID gate]
    end
```

## Credential Namespaces

| Namespace | Status | Description |
|-----------|--------|-------------|
| `ssh-agent` | Phase 1 | SSH key operations for git, SCP, remote access |
| `aws-credentials` | Phase 2 | AWS SDK credential vending via STS role assumption |

## Security Properties

- SSH private keys never enter the VM — only signatures cross the bus
- AWS long-lived credentials never leave the host
- AWS STS session tokens are short-lived and role-scoped
- Optional Touch ID approval gate for sign operations
- All operations logged to host-side audit log

## Quick Start

See [Getting Started](getting-started/quick-start.md) for setup instructions.
