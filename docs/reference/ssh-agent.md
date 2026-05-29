# SSH Agent

The `ssh-agent` namespace brokers SSH key operations between the shed microVM and the host machine.

## How It Works

`shed-ext-ssh-agent` runs inside the VM as a systemd service, listening on a Unix domain socket at `/run/shed-extensions/ssh-agent.sock`. The `SSH_AUTH_SOCK` environment variable points all SSH clients to this socket.

When an SSH client (git, ssh, scp) requests a key operation, `shed-ext-ssh-agent` translates it into a message bus request and forwards it to the host agent for processing.

## Operations

| Operation | Description | Touch ID |
|-----------|-------------|----------|
| `list` | Return available public keys | No |
| `sign` | Sign challenge data with specified key | Configurable |

## Message Format

### Request

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

### Response

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

### Error

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

## Host-Side Backend

The host agent auto-detects the best signing strategy:

1. **Agent-forward** (default): If `SSH_AUTH_SOCK` exists on the host, proxies sign requests to the developer's existing SSH agent (Secretive, 1Password, ssh-agent, etc.)
2. **Local-keys** (fallback): Reads keys directly from `~/.ssh/`

Override via config:

```yaml
ssh:
  mode: agent-forward  # or "local-keys"
```

## Touch ID Approval

Sign operations can require interactive approval on the host. Set `ssh.approval.enabled: true` to gate every sign request behind a macOS authentication prompt (see [Configuration](configuration.md#ssh-settings) for all fields).

When enabled, `ssh.approval.method` selects the authentication factor:

| `method` | LocalAuthentication policy | Behavior |
|----------|----------------------------|----------|
| `biometrics-or-password` (default) | `LAPolicyDeviceOwnerAuthentication` | Touch ID, Apple Watch, or account password. Works in clamshell mode (lid closed) and on Macs without a fingerprint sensor. |
| `biometrics` | `LAPolicyDeviceOwnerAuthenticationWithBiometrics` | Touch ID only. Fails when no biometric is available. |

`biometrics-or-password` is the default so approval works out of the box on a closed-lid MacBook with an external display, on a Mac mini/Studio, and on any setup without a Touch ID Magic Keyboard.

The `ssh.approval.policy` field (`per-request`, `per-session`, `per-shed`) controls how often the prompt appears, and `session_ttl` sets how long an approval is cached. `LAPolicyDeviceOwnerAuthentication` also has its own macOS-managed grace period that can briefly skip the prompt; this is independent of `session_ttl`, and the two compose without conflict.

## Timeouts

Credential requests use a 3-second timeout. On timeout, `shed-ext-ssh-agent` logs an actionable error:

```text
ERROR: ssh-agent sign request timed out — shed-host-agent may not be running.
```

## Startup Health Check

On startup, `shed-ext-ssh-agent` publishes a ping to the `ssh-agent` namespace. If no response arrives within 2 seconds, it logs a warning but continues starting (so it's ready when the host agent comes up).
