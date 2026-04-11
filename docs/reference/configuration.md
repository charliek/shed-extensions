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

aws:
  source_profile: default
  default_role: arn:aws:iam::123456789012:role/smartthings-dev
  session_duration: 1h
  cache_refresh_before: 5m

  # Per-shed role overrides
  sheds:
    my-service:
      role: arn:aws:iam::123456789012:role/smartthings-dev
    integration-tests:
      role: arn:aws:iam::123456789012:role/smartthings-staging-readonly

docker:
  registries:
    - us-docker.pkg.dev
    - ghcr.io
    - artifactory.corp.com
  # allow_all: true         # bypass allowlist
  # config_path: ~/.docker/config.json  # override Docker config location

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

### AWS Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `aws.source_profile` | string | `default` | AWS credentials profile to use for AssumeRole |
| `aws.default_role` | string | | IAM role ARN to assume (required if any shed needs AWS) |
| `aws.session_duration` | string | `1h` | STS session token lifetime |
| `aws.cache_refresh_before` | string | `5m` | Refresh cached credentials when less than this time remains |
| `aws.sheds.<name>.role` | string | | Per-shed IAM role ARN override |

### Docker Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `docker.registries` | []string | `[]` | Registry hostnames to allow credential brokering for |
| `docker.allow_all` | bool | `false` | Allow credentials for any registry (bypasses allowlist) |
| `docker.config_path` | string | (auto-detect) | Override Docker config.json path. If unset, checks `$DOCKER_CONFIG/config.json` first, then `~/.docker/config.json` |

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

- `SSH_AUTH_SOCK=/run/shed-extensions/ssh-agent.sock` via `/etc/environment.d/shed-extensions.conf`
- `AWS_CONTAINER_CREDENTIALS_FULL_URI=http://127.0.0.1:499/credentials` via `/etc/environment.d/shed-extensions.conf`
- `shed-ext-ssh-agent` and `shed-ext-aws-credentials` start via systemd at boot
- `docker-credential-shed` available on PATH; `~/.docker/config.json` configured with `"credsStore": "shed"`

## CLI Flags

### shed-host-agent

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `~/.config/shed/extensions.yaml` | Path to config file |

### shed-ext-ssh-agent

| Flag | Default | Description |
|------|---------|-------------|
| `--sock` | `/run/shed-extensions/ssh-agent.sock` | Unix socket path |
| `--publish-url` | `http://127.0.0.1:498/v1/publish` | shed-agent publish endpoint |

### shed-ext-aws-credentials

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `499` | HTTP listen port |
| `--publish-url` | `http://127.0.0.1:498/v1/publish` | shed-agent publish endpoint |

### docker-credential-shed

| Flag / Env | Default | Description |
|------------|---------|-------------|
| `SHED_PUBLISH_URL` | `http://127.0.0.1:498/v1/publish` | shed-agent publish endpoint (env var) |

Commands: `get` (resolve credentials), `list` (list registries), `store` (rejected), `erase` (rejected), `version`.

