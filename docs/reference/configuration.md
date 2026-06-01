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
    # method: biometrics-or-password   # biometrics-or-password | biometrics
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
| `ssh.approval.method` | string | `biometrics-or-password` | Auth method: `biometrics-or-password` (Touch ID, Apple Watch, or account password — works in clamshell mode and on Macs without a sensor) or `biometrics` (Touch ID only) |
| `ssh.approval.session_ttl` | string | `4h` | How long a session approval remains valid |

### AWS Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `aws.source_profile` | string | `default` | AWS credentials profile to use for AssumeRole |
| `aws.default_role` | string | | IAM role ARN to assume (required if any shed needs AWS) |
| `aws.session_duration` | string | `1h` | STS session token lifetime |
| `aws.cache_refresh_before` | string | `5m` | Refresh cached credentials when less than this time remains |
| `aws.sheds.<name>.role` | string | | Deprecated global per-shed role override (any server). Prefer `aws.servers.…` |
| `aws.servers.<server>.default_role` | string | | Per-server role default (multi-server mode) |
| `aws.servers.<server>.sheds.<shed>.role` | string | | Per-server, per-shed role override (most specific) |

### Docker Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `docker.registries` | []string | `[]` | Registry hostnames to allow credential brokering for |
| `docker.allow_all` | bool | `false` | Allow credentials for any registry (bypasses allowlist) |
| `docker.config_path` | string | (auto-detect) | Override Docker config.json path. If unset, checks `$DOCKER_CONFIG/config.json` first, then `~/.docker/config.json` |
| `docker.servers.<server>.registries` / `.allow_all` | []string / bool | | Per-server allowlist override (multi-server mode) |
| `docker.servers.<server>.sheds.<shed>.registries` / `.allow_all` | []string / bool | | Per-server, per-shed allowlist override (most specific) |

### Logging Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `logging.enabled` | bool | `true` | Enable audit logging |
| `logging.path` | string | `~/.local/share/shed/extensions-audit.log` | Path to audit log file |

### Server Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `server` | string | `http://localhost:8080` | shed-server URL. Used only in single-server mode (when `discovery:` is omitted) |

## Multi-Server Discovery

A single `shed-host-agent` process can broker credentials for **many** shed
servers at once, instead of running one process per server. Add a `discovery:`
block — its presence switches the agent into multi-server mode (and `server:`
above is then ignored). Servers are discovered from shed's own CLI config
(`~/.shed/config.yaml`, the same `servers:` you manage with `shed server add`);
a server's broker URL is `http://<host>:<http_port>`.

```yaml
discovery:
  # "all" (default) watches every server in ~/.shed/config.yaml.
  # A list watches only those by name:
  #   servers: [mini2, mini3]
  servers: all

  # Where to discover servers (default ~/.shed/config.yaml)
  # source: ~/.shed/config.yaml

  # How changes are picked up without a restart:
  #   fsnotify (default) — react instantly to ~/.shed/config.yaml changes
  #   poll               — re-read on an interval
  #   off                — read once at startup
  watch: fsnotify
  # poll_interval: 10s    # used when watch: poll
  # debounce: 500ms       # used when watch: fsnotify
```

When a server is added to (or removed from) `~/.shed/config.yaml`, the agent
starts (or stops) brokering for it automatically — no restart needed. Offline
servers are retried in the background and cost nothing until they come up.

### Discovery Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `discovery.servers` | `all` \| []string | `all` | Which discovered servers to watch — every server, or a named subset |
| `discovery.source` | string | `~/.shed/config.yaml` | shed CLI config to discover servers from |
| `discovery.watch` | string | `fsnotify` | Live-reload mechanism: `fsnotify`, `poll`, or `off`. Falls back to polling if fsnotify can't start |
| `discovery.poll_interval` | string | `10s` | Reconcile cadence when `watch: poll` |
| `discovery.debounce` | string | `500ms` | Event coalescing window when `watch: fsnotify` |

### Per-server and per-shed overrides

In multi-server mode the `aws:` and `docker:` blocks are **hierarchical**: the
top-level fields are the defaults, and a nested `servers:` tree overrides them
per server and per shed (most specific wins). This also keeps identical shed
names on different servers isolated.

```yaml
aws:
  source_profile: default                       # process-global
  default_role: arn:aws:iam::111:role/Dev        # default for every server/shed
  session_duration: 1h
  servers:
    mini2:
      default_role: arn:aws:iam::111:role/Mini2  # per-server default
      sheds:
        web:
          role: arn:aws:iam::111:role/Web        # per-shed (wins)

docker:
  registries: [ghcr.io]                          # default allowlist
  allow_all: false
  servers:
    mini2:
      allow_all: true                            # per-server
      sheds:
        web:
          registries: [ghcr.io, us-central1-docker.pkg.dev]   # per-shed
```

Resolution order (later overrides earlier): top-level defaults →
`aws.sheds.<shed>` (deprecated global, AWS only) → `…servers.<server>` →
`…servers.<server>.sheds.<shed>`. For Docker, a non-`null` `registries` list
**replaces** (does not merge) the inherited list, and `allow_all` is only
overridden when explicitly set at a more specific level.

All audit log lines and shed-desktop events carry a `server` field so activity
from different servers stays distinguishable in the combined log.

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

