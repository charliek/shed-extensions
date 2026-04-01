# Configuration

## Host-Side Configuration

The host agent reads configuration from `~/.config/shed/extensions.yaml`.

```yaml
# shed-server URL
server: http://localhost:8080

ssh:
  # SSH backend: "agent-forward", "local-keys", or "" (auto-detect)
  # mode: ""

  # Touch ID / biometric approval
  approval:
    enabled: false
    # policy: per-session     # per-request | per-session | per-shed
    # session_ttl: 4h

# Audit logging
logging:
  enabled: true
  path: ~/.local/share/shed/extensions-audit.log
```

### SSH Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `ssh.mode` | string | `""` (auto) | SSH backend mode. Auto-detect selects agent-forward if `SSH_AUTH_SOCK` exists, falls back to local-keys. |
| `ssh.approval.enabled` | bool | `false` | Enable Touch ID approval gate for sign operations |
| `ssh.approval.policy` | string | `per-session` | Approval policy: `per-request`, `per-session`, `per-shed` |
| `ssh.approval.session_ttl` | string | `4h` | How long a session approval remains valid |

### Logging Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `logging.enabled` | bool | `true` | Enable audit logging |
| `logging.path` | string | `~/.local/share/shed/extensions-audit.log` | Path to audit log file |

### Server Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `server` | string | `http://localhost:8080` | shed-server URL |

## Guest-Side Configuration

None. The opinionated base image configures everything:

- `SSH_AUTH_SOCK=/run/shed-ssh-agent.sock` via `/etc/environment.d/shed-extensions.conf`
- `shed-ssh-agent` starts via systemd at boot

## CLI Flags

### shed-host-agent

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `~/.config/shed/extensions.yaml` | Path to config file |

### shed-ssh-agent

| Flag | Default | Description |
|------|---------|-------------|
| `--sock` | `/run/shed-ssh-agent.sock` | Unix socket path |
| `--publish-url` | `http://127.0.0.1:498/v1/publish` | shed-agent publish endpoint |
