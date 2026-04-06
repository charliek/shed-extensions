# shed-extensions: Credential Isolation for Development Environments

## Overview

shed-extensions provides secure credential brokering between shed microVMs and the developer's host machine. Credentials never enter the VM — all signing and secret resolution happens on the host, mediated by shed's plugin message bus.

The PoC implements two credential namespaces that cover the primary developer workflows:

- **`ssh-agent`** — SSH key operations for git, SCP, and remote access
- **`aws-credentials`** — AWS SDK credential vending via STS role assumption

Both integrate through standard protocol hooks (SSH agent socket, AWS container credential endpoint) requiring **zero application code changes** inside the VM.

## Problem Statement

Today, developers working inside shed microVMs need credential access for two primary workflows:

1. **Git operations** — pushing/pulling from GitHub requires SSH keys or tokens
2. **AWS service access** — running and testing SmartThings services locally requires AWS credentials for DynamoDB, S3, SQS, Secrets Manager, etc.

Current approaches (copying key files into the VM, mounting `~/.aws/credentials`, forwarding SSH agents over SSH) all result in credential material existing inside the execution environment. This creates risk surface for accidental exposure, complicates credential rotation, and makes audit difficult.

## Goals

- Credentials never exist as files, environment variables, or memory inside the VM
- Developers experience zero friction — standard tools (git, AWS SDK) work without configuration
- Host-side policy enforcement: role scoping, audit logging, optional biometric approval
- No changes to existing application code or service configurations
- Clear security posture improvement that can be articulated to InfoSec

## Non-Goals (PoC)

- Multi-user or shared credential management
- Centralized team credential service (future enhancement)
- Credential rotation automation
- Support for non-macOS hosts (Linux host support is a fast-follow)
- GCP, Azure, or other cloud provider credential brokering
- Automatic host agent startup (launchd integration is post-PoC)
- shed-server integration (host agent runs standalone for the PoC)

## Architecture

### Component Overview

```
┌─────────────────────────────────────────────────────┐
│  shed microVM (Linux guest)                         │
│                                                     │
│  ┌──────────────┐     ┌──────────────────────────┐  │
│  │  SSH client   │────▶│  shed-ext-ssh-agent           │  │
│  │  (git push)   │     │  Unix socket listener     │  │
│  └──────────────┘     │  SSH_AUTH_SOCK             │  │
│                        │       │                    │  │
│  ┌──────────────┐     │       ▼                    │  │
│  │  AWS SDK      │     │  POST 127.0.0.1:498       │  │
│  │  (any lang)   │     │  /v1/publish              │  │
│  └──────┬───────┘     └──────────────────────────┘  │
│         │                                           │
│         ▼                                           │
│  ┌──────────────────────────────────────────────┐   │
│  │  shed-ext-aws-credentials                               │   │
│  │  HTTP server on 127.0.0.1:499                 │   │
│  │  AWS_CONTAINER_CREDENTIALS_FULL_URI           │   │
│  │       │                                       │   │
│  │       ▼                                       │   │
│  │  POST 127.0.0.1:498/v1/publish                │   │
│  └──────────────────────────────────────────────┘   │
│                        │                             │
│                   vsock (port 1026)                   │
│                        │                             │
└────────────────────────┼─────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────┐
│  shed-server (host)                                  │
│  Plugin message bus router                           │
│       │                                              │
│       ▼ SSE                                          │
│  ┌──────────────────────────────────────────────┐   │
│  │  shed-host-agent                              │   │
│  │                                               │   │
│  │  ┌─────────────────┐  ┌────────────────────┐  │   │
│  │  │  SSH handler     │  │  AWS handler       │  │   │
│  │  │  ~/.ssh keys     │  │  ~/.aws/credentials│  │   │
│  │  │  Touch ID gate   │  │  sts:AssumeRole    │  │   │
│  │  └─────────────────┘  └────────────────────┘  │   │
│  └──────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────┘
```

### Guest-Side Binaries

**`shed-ext-ssh-agent`** — Implements the SSH agent protocol (`golang.org/x/crypto/ssh/agent`), listens on a Unix domain socket, and translates `Sign()` calls into message bus requests. Runs as a systemd service in the VM.

**`shed-ext-aws-credentials`** — Implements the AWS container credential endpoint (HTTP server) and translates SDK credential requests into message bus requests. Runs as a systemd service in the VM. The proxy is a passthrough — it does not cache credentials (see [Credential Caching](#credential-caching)).

Both binaries are pure Go with no platform-specific dependencies. They cross-compile to `linux/amd64` and `linux/arm64` from any OS and ship pre-installed in the extensions-enabled base image.

### Host-Side Binary

**`shed-host-agent`** — Single binary that subscribes to multiple namespaces via SSE. Each namespace handler is a pluggable module. macOS-specific features (Touch ID) use build tags. The handler interface is designed so handlers can migrate into shed-server as an optional module in the future without rewriting handler logic.

One host-agent instance serves all running sheds. The message bus envelope's `Shed` field identifies which shed made each request. The host handler uses this for role mapping, audit logging, and policy enforcement.

### Guest-Side Deployment

Guest-side binaries ship as part of an opinionated shed base image. No runtime configuration or feature flags — if you use this image, credential isolation is on by default.

The base image includes:

- `shed-ext-ssh-agent` and `shed-ext-aws-credentials` binaries installed to `/usr/local/bin/`
- Systemd unit files that start both services at boot
- Environment variables (`SSH_AUTH_SOCK`, `AWS_CONTAINER_CREDENTIALS_FULL_URI`) set globally via `/etc/environment.d/`
- Health checks that report status back through the message bus

A developer creates a shed with the extensions-enabled image and everything just works — git pushes, AWS SDK calls — with no setup on the guest side.

**Systemd units:**

```ini
# shed-ext-ssh-agent.service
[Unit]
Description=shed SSH Agent (credential proxy)
After=network.target shed-agent.service
Wants=shed-agent.service

[Service]
Type=simple
User=shed
Group=shed
RuntimeDirectory=shed-extensions
ExecStart=/usr/local/bin/shed-ext-ssh-agent \
  --sock /run/shed-extensions/ssh-agent.sock \
  --publish-url http://127.0.0.1:498/v1/publish
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

```ini
# shed-ext-aws-credentials.service
[Unit]
Description=shed AWS Credential Proxy
After=network.target shed-agent.service
Wants=shed-agent.service

[Service]
Type=simple
User=shed
Group=shed
RuntimeDirectory=shed-extensions
AmbientCapabilities=CAP_NET_BIND_SERVICE
ExecStart=/usr/local/bin/shed-ext-aws-credentials \
  --port 499 \
  --publish-url http://127.0.0.1:498/v1/publish
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

**Environment variables** (`/etc/environment.d/shed-extensions.conf`):

```
SSH_AUTH_SOCK=/run/shed-extensions/ssh-agent.sock
AWS_CONTAINER_CREDENTIALS_FULL_URI=http://127.0.0.1:499/credentials
```

Both guest services depend on `shed-agent.service` because they need the message bus (the HTTP publish endpoint on `127.0.0.1:498`) to be available. Systemd's `After=shed-agent.service` ensures correct startup ordering. `Restart=always` with a 2-second backoff handles transient failures during boot.

### Host-Side Deployment

The developer manually starts `shed-host-agent`. It connects to shed-server's plugin API and subscribes to the credential namespaces.

```bash
shed-host-agent --config ~/.config/shed/extensions.yaml
```

## Namespace: `ssh-agent`

### Request Envelope

```json
{
  "id": "0192b3a4-...",
  "namespace": "ssh-agent",
  "type": "request",
  "payload": {
    "operation": "sign",
    "public_key": "ssh-ed25519 AAAA... user@host",
    "data": "<base64-encoded challenge>",
    "flags": 0
  }
}
```

### Response Envelope

```json
{
  "id": "0192b3a4-...",
  "namespace": "ssh-agent",
  "type": "response",
  "payload": {
    "format": "ssh-ed25519",
    "blob": "<base64-encoded signature>",
    "rest": ""
  }
}
```

### Operations

| Operation | Description | Touch ID |
|-----------|-------------|----------|
| `list` | Return available public keys | No |
| `sign` | Sign challenge data with specified key | Configurable |

### Error Response

```json
{
  "id": "0192b3a4-...",
  "namespace": "ssh-agent",
  "type": "response",
  "payload": {
    "error": "key not found",
    "code": "KEY_NOT_FOUND"
  }
}
```

### Host-Side Key Resolution

The host handler automatically detects the best signing strategy at startup. No configuration required for the common case.

```go
func resolveSSHBackend() SSHBackend {
    if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
        if _, err := os.Stat(sock); err == nil {
            // Developer has an SSH agent running (Secretive, 1Password, ssh-agent, etc.)
            return NewAgentForwardBackend(sock)
        }
    }
    // No agent running — read keys directly from ~/.ssh/
    return NewLocalKeyBackend()
}
```

**Agent-forward mode** (auto-selected when host has `SSH_AUTH_SOCK`): Proxies sign requests to the developer's existing SSH agent. This respects whatever key management they already use — Secretive, 1Password SSH agent, yubikey-agent, or plain ssh-agent. The host agent is a transparent layer that adds audit logging and optional policy without disrupting the developer's setup.

**Local-key mode** (fallback when no host agent is running): Reads keys from `~/.ssh/` directly. Scans for standard key files (`id_ed25519`, `id_rsa`, etc.).

The developer can override auto-detection via config if needed, but the PoC optimizes for zero-config.

### Touch ID / Biometric Approval

Touch ID is available as an optional approval gate for SSH sign operations. It is disabled by default and enabled via `approval.enabled` in the host config. When enabled, the default policy is per-session with a 4-hour TTL — one Touch ID prompt approves all sign operations for the session duration. Other policies (per-request, per-shed) are configurable.

The Touch ID integration uses macOS `LocalAuthentication` framework via cgo, gated behind darwin build tags. Non-macOS builds compile with a no-op stub.

## Namespace: `aws-credentials`

### Request Envelope

```json
{
  "id": "0192b3a5-...",
  "namespace": "aws-credentials",
  "type": "request",
  "payload": {
    "operation": "get_credentials"
  }
}
```

The request is intentionally minimal — the shed's configured role determines what credentials are returned. The VM doesn't get to choose which role it receives.

### Response Envelope

```json
{
  "id": "0192b3a5-...",
  "namespace": "aws-credentials",
  "type": "response",
  "payload": {
    "access_key_id": "ASIAIOSFODNN7EXAMPLE",
    "secret_access_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
    "session_token": "FwoGZXIvYXdzE...",
    "expiration": "2026-03-31T19:00:00Z"
  }
}
```

### AWS SDK Integration

The AWS SDK credential chain checks `AWS_CONTAINER_CREDENTIALS_FULL_URI` automatically across all languages (Java, Python, Go, Node, Kotlin). The in-VM proxy serves the response in the exact format the SDK expects:

```
GET http://127.0.0.1:499/credentials
→ 200 OK
{
  "AccessKeyId": "ASIA...",
  "SecretAccessKey": "...",
  "Token": "...",
  "Expiration": "2026-03-31T19:00:00Z"
}
```

The SDK handles refresh automatically — when credentials approach expiration, the SDK re-requests, which triggers another round trip through the bus.

### Role Configuration

Each shed maps to a specific IAM role via the host-side configuration:

```yaml
# ~/.config/shed/extensions.yaml
aws:
  source_profile: default        # which ~/.aws/credentials profile to use for AssumeRole
  default_role: arn:aws:iam::123456789:role/smartthings-dev
  
  # per-shed overrides
  sheds:
    my-service:
      role: arn:aws:iam::123456789:role/smartthings-dev
    integration-tests:
      role: arn:aws:iam::123456789:role/smartthings-staging-readonly
```

### Credential Caching

The guest-side proxy (`shed-ext-aws-credentials`) does not cache credentials. Every SDK request passes through the message bus to the host handler, which maintains a single credential cache per shed per role.

The message bus round trip is sub-millisecond (vsock, same machine, no network stack), and the AWS SDKs only fetch credentials when their in-memory cache is stale — roughly once per hour. A single host-side cache avoids cache coherence complexity between layers.

**Host cache behavior:**

1. AWS SDK inside the VM calls `GET http://127.0.0.1:499/credentials`
2. `shed-ext-aws-credentials` publishes a request to `aws-credentials` namespace via the message bus
3. Host handler checks its cache — if valid STS credentials exist with >5 minutes remaining, return immediately
4. If cache is stale, call `sts:AssumeRole`, cache the result, return
5. Response flows back through the bus to the proxy, which returns it to the SDK
6. The SDK caches the credential in memory and manages its own refresh timing

**Host cache parameters** (configurable):

```yaml
aws:
  session_duration: 1h          # STS token lifetime
  cache_refresh_before: 5m      # refresh cache when < 5 min remaining
```

The SDK's built-in refresh logic (5-15 minutes before expiry depending on language) drives re-fetching. The host cache ensures the STS API is only called once per refresh cycle even if multiple SDK clients in the same shed request simultaneously.

### STS Session Details

- Session name format: `shed-{shed-name}-{timestamp}` for CloudTrail traceability
- Default session duration: 1 hour (configurable)
- If `AssumeRole` fails (expired source credentials, role doesn't exist), a clear error propagates back to the SDK inside the VM

## Error Handling

### Failure Modes

| Failure | Behavior | Resolution |
|---------|----------|------------|
| Host agent not running | 3-second timeout, actionable error message | Start `shed-host-agent` on host Mac |
| Host agent denies request | Fast error response with reason | Check config, key availability, or role mapping |
| Host handler fails (STS error, key missing) | Fast error response with detail | Fix host-side credentials or config |
| Message bus not connected (shed-agent issue) | Immediate connection error at startup | Check shed-agent status |

### Startup Health Check

When `shed-ext-ssh-agent` and `shed-ext-aws-credentials` start (via systemd), each publishes a ping message to its namespace and waits up to 2 seconds for a response.

If the host agent responds, the service starts normally and logs confirmation. If no response arrives within 2 seconds, the service starts anyway (so it's ready when the host agent comes up later) but logs a clear warning:

```
WARNING: shed-host-agent not connected for namespace 'ssh-agent'.
SSH operations will fail until shed-host-agent is running on your Mac.
Start it with: shed-host-agent --config ~/.config/shed/extensions.yaml
```

This warning is visible in `journalctl` and also written to `/run/shed-extensions/*.status` for programmatic consumption.

### Request Timeout

Credential requests use a 3-second timeout (instead of the default 30-second message bus timeout). On timeout, the guest binary returns an error with actionable text.

**SSH agent timeout** — the SSH client shows its standard "Permission denied (publickey)" error, but `shed-ext-ssh-agent` also logs:

```
ERROR: ssh-agent sign request timed out — shed-host-agent may not be running.
Check with: journalctl -u shed-ext-ssh-agent
```

**AWS proxy timeout** — returned as an HTTP response the SDK can surface:

```json
HTTP 503 Service Unavailable
{
  "error": "credential request timed out",
  "message": "shed-host-agent not reachable. Is it running on your Mac?",
  "hint": "Start it with: shed-host-agent --config ~/.config/shed/extensions.yaml"
}
```

### Extension Health (via shed-agent)

Extension health is now reported by shed-agent and visible via `shed list -vv` on the host:

```text
Extensions:
  aws-credentials:     guest=running  host=connected
  ssh-agent:           guest=running  host=connected
```

When the host agent is not running:

```text
Extensions:
  aws-credentials:     guest=running  host=unreachable
  ssh-agent:           guest=running  host=unreachable
```

## Configuration

### Host-Side Configuration

```yaml
# ~/.config/shed/extensions.yaml

ssh:
  # SSH backend is auto-detected (agent-forward if SSH_AUTH_SOCK exists,
  # local-keys fallback). Override only if needed:
  # mode: agent-forward | local-keys

  # Optional: restrict which keys can be used from sheds
  # allowed_keys:
  #   - SHA256:abc123...       # fingerprint allowlist

  # Touch ID / biometric approval (disabled by default)
  approval:
    enabled: false
    # policy: per-session      # per-request | per-session | per-shed
    # session_ttl: 4h

aws:
  source_profile: default
  default_role: arn:aws:iam::123456789:role/smartthings-dev
  session_duration: 1h
  cache_refresh_before: 5m

  sheds:
    my-service:
      role: arn:aws:iam::123456789:role/smartthings-dev

# Audit logging
logging:
  enabled: true
  path: ~/.local/share/shed/extensions-audit.log
```

### Guest-Side Configuration

None. The opinionated base image configures everything via convention:

- `SSH_AUTH_SOCK=/run/shed-extensions/ssh-agent.sock` — set in `/etc/environment.d/`
- `AWS_CONTAINER_CREDENTIALS_FULL_URI=http://127.0.0.1:499/credentials` — set in `/etc/environment.d/`
- Both services start via systemd at boot

No per-shed or per-developer guest-side configuration is needed.

## Security Properties

### What Enters the VM

| Data | Enters VM? | Notes |
|------|-----------|-------|
| SSH private keys | No | Only signatures cross the bus |
| SSH public keys | Yes | Returned by `list` operation |
| AWS long-lived credentials | No | Never leave the host |
| AWS STS session tokens | Yes | Short-lived, scoped to specific role |
| AWS role ARN | No | Determined by host-side config |
| Touch ID biometric data | No | Evaluated entirely on host hardware |

### Threat Model

**Compromised VM**: An attacker with root in the VM can request signatures and AWS credentials, but cannot extract private keys or escalate to roles not configured for that shed. STS tokens expire and cannot be refreshed without the host agent. Audit logs on the host capture all requests.

**Compromised host agent**: Has access to the developer's SSH keys and AWS credentials. This is equivalent to the current threat model (developer's machine is compromised). No regression.

**Bus interception (vsock)**: The vsock transport is point-to-point between the VM and host kernel. There is no network path to intercept. Other VMs cannot access a shed's vsock connection.

**Replay attacks**: Each request has a unique UUIDv7 ID. The host handler rejects duplicate IDs within a time window.

### Audit Logging

All credential operations are logged as JSON lines to the host filesystem:

```json
{"ts":"2026-03-31T15:04:05Z","shed":"my-service","ns":"ssh-agent","op":"sign","result":"ok","detail":"SHA256:abc123","approval":"none"}
{"ts":"2026-03-31T15:04:06Z","shed":"my-service","ns":"aws-credentials","op":"get_credentials","result":"ok","detail":"arn:aws:iam::123:role/dev","approval":"none"}
```

Fields: `ts` (timestamp), `shed` (shed name), `ns` (namespace), `op` (operation), `result` (ok/denied/error), `detail` (key fingerprint or role ARN), `approval` (touchid/none/cached).

## Project Structure

```
shed-extensions/
├── cmd/
│   ├── shed-host-agent/           # Host-side: single binary, all handlers
│   │   ├── main.go
│   │   ├── config.go              # Parse extensions.yaml
│   │   ├── ssh_handler.go         # ssh-agent namespace handler
│   │   ├── ssh_backend.go         # Auto-detect: agent-forward vs local-keys
│   │   ├── aws_handler.go         # aws-credentials namespace handler
│   │   ├── touchid_darwin.go      # Touch ID via LocalAuthentication
│   │   └── touchid_stub.go        # No-op for non-Darwin builds
│   ├── shed-ext-ssh-agent/        # Guest-side: SSH agent protocol adapter
│   │   └── main.go
│   └── shed-ext-aws-credentials/  # Guest-side: AWS credential endpoint
│       └── main.go
├── internal/
│   ├── protocol/                  # Namespace-specific request/response types
│   │   ├── ssh.go
│   │   └── aws.go
│   ├── sshagent/                  # agent.Agent implementation (uses sdk.BusClient)
│   │   ├── agent.go
│   │   └── agent_test.go
│   ├── awsproxy/                  # AWS credential endpoint (uses sdk.BusClient)
│   │   ├── proxy.go
│   │   └── proxy_test.go
│   └── testutil/                  # Test helpers
│       └── mockbus.go
├── image/                         # Base image overlay files
│   ├── etc/
│   │   ├── environment.d/
│   │   │   └── shed-extensions.conf
│   │   └── systemd/system/
│   │       ├── shed-ext-ssh-agent.service
│   │       └── shed-ext-aws-credentials.service
│   └── README.md
├── docs/
│   ├── spec.md
│   ├── getting-started/
│   ├── reference/
│   └── development/
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

### Build Targets

```makefile
.PHONY: build-guest
build-guest:
	GOOS=linux GOARCH=arm64 go build -o dist/linux-arm64/shed-ext-ssh-agent ./cmd/shed-ext-ssh-agent
	GOOS=linux GOARCH=arm64 go build -o dist/linux-arm64/shed-ext-aws-credentials ./cmd/shed-ext-aws-credentials
	GOOS=linux GOARCH=amd64 go build -o dist/linux-amd64/shed-ext-ssh-agent ./cmd/shed-ext-ssh-agent
	GOOS=linux GOARCH=amd64 go build -o dist/linux-amd64/shed-ext-aws-credentials ./cmd/shed-ext-aws-credentials

.PHONY: build-host
build-host:
	go build -o dist/shed-host-agent ./cmd/shed-host-agent
```

The host binary requires a macOS build environment (cgo for Touch ID). Guest binaries are pure Go and cross-compile from any OS. How binaries and the `image/` overlay get into the VM image is handled by the shed image build process. For PoC testing, manually copying into a running shed and enabling the systemd units is sufficient.

## Implementation Plan

### Phase 1: SSH Agent

1. Scaffold the `shed-extensions` repo, Go module, Makefile with cross-compile targets
2. Implement `internal/hostclient/` — SSE client for shed-server plugin API (shared by all host handlers)
3. Implement `internal/sshagent/` — the `agent.Agent` adapter that publishes sign requests to the message bus
4. Implement `cmd/shed-ext-ssh-agent/` — Unix socket listener, systemd-ready, startup health check ping, 3-second request timeout with actionable error messages
5. Implement `cmd/shed-host-agent/` with SSH handler — SSE subscription to `ssh-agent` namespace
6. Implement auto-detect SSH backend (agent-forward if `SSH_AUTH_SOCK` exists, local-keys fallback)
7. Add Touch ID support behind `approval.enabled` config flag (darwin build tags)
8. Prepare `image/` overlay directory (systemd unit, environment config)
9. End-to-end test: `git clone` / `git push` from inside a running shed

### Phase 2: AWS Credentials

1. Implement `internal/awsproxy/` — HTTP credential endpoint (passthrough, no guest-side caching), serving AWS SDK response format
2. Implement `cmd/shed-ext-aws-credentials/` — HTTP server on port 499, message bus integration, systemd-ready, startup health check, 3-second request timeout
3. Add AWS handler to `shed-host-agent` — STS AssumeRole, role mapping from config, host-side credential caching with configurable refresh window
4. Add `extensions.yaml` configuration parsing for AWS role mappings and cache parameters
5. Update `image/` overlay with `shed-ext-aws-credentials` systemd unit and `AWS_CONTAINER_CREDENTIALS_FULL_URI` environment variable
6. End-to-end test: AWS SDK call from inside a shed (Go and Python at minimum, Java if time permits)

### Phase 3: Polish & Demo

1. Audit logging (JSON lines to `~/.local/share/shed/extensions-audit.log`)
2. Extension health reporting via shed-agent (guest/host status in `shed list -vv`)
3. Getting-started documentation
4. Demo script for stakeholder presentations (InfoSec, ACE, engineering leadership)
5. Security posture comparison document (before/after credential isolation)

### Phase 4: Integration Testing ✅

Validate the full system in a real VZ-backed shed on macOS. No image building — binaries mounted via `--local-dir` and installed manually.

1. Build all binaries (shed-server, guest linux/arm64, host agent)
2. Create a test shed with `--local-dir` mounting the guest binary dist directory
3. Install guest binaries, systemd units, and environment config inside the VM
4. Start `shed-host-agent` on the host, verify bus connectivity with `shed list -vv`
5. End-to-end SSH test: `ssh-add -l`, `ssh -T git@github.com`, `git clone` from inside the VM
6. End-to-end AWS test: `curl` credential endpoint, verify STS credentials returned
7. Verify no private keys or long-lived AWS credentials exist in the VM
8. Verify audit log captures all operations
9. Fix bugs found during testing (socket permissions, SCP support, environment.d loading)

### Phase 5: Experimental Image

Build guest binaries into a distributed experimental Docker image so developers get credential isolation out of the box — no manual setup inside the VM.

1. Create a Dockerfile or image build process that layers extensions onto the base shed image
2. Bake in: guest binaries (`shed-ext-ssh-agent`, `shed-ext-aws-credentials`), systemd units, extension manifests, environment.d config
3. Push to registry as an extensions-enabled image tag (e.g., `shed-base:extensions`)
4. Update shed-server config to reference the extensions image
5. End-to-end validation: `shed create myproject --image extensions` works with zero in-VM setup
6. Document the image build process and developer quickstart

### Phase 6: Integration & Distribution

Production-readiness: auto-start, shed-server integration, and graduation from experimental to standard.

1. launchd plist for `shed-host-agent` auto-start on macOS
2. Evaluate integrating host handlers into shed-server as an optional module
3. Explore injecting guest binaries via 9P/VirtioFS (avoid image rebuilds for updates)
4. Graduate from experimental image tag to optional feature in standard image

## Graduation Path

The feature follows an experimental → optional → default model:

1. **Experimental** (Phase 5): Extensions-enabled base image as a separate image tag. Developer manually runs host agent. Used for dogfooding and feedback.
2. **Optional** (Phase 6): Guest binaries available in the standard image but disabled by default. Host agent handlers available as a shed-server flag (`--enable-extensions`). launchd auto-start for host agent.
3. **Default** (future): Credential isolation on by default for new sheds. Host agent integrated into shed-server. Standard security posture for the org.

Each stage requires demonstrated stability and positive developer feedback before progression.

## Success Criteria

### Functional

- A developer can `git push` from inside a shed without any SSH keys in the VM
- A developer can run a service that makes AWS API calls without any AWS credentials in the VM
- Both work with zero changes to existing application code or service configuration
- AWS credentials are scoped to a specific IAM role per shed
- A developer using Secretive, 1Password, or standard ssh-agent on their Mac has a seamless experience with no extra configuration

### Security

- No private key material (SSH or AWS long-lived) exists anywhere in the VM at any time
- AWS STS tokens in the VM are short-lived (1 hour) and role-scoped
- Audit log on the host captures every credential operation with shed name and timestamp

### Demo

- Compelling enough for InfoSec to see a concrete security posture improvement
- Compelling enough for engineering leadership to justify further investment via ACE initiative
- Compelling enough for individual developers to want to use it for daily work
