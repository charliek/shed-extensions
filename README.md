# shed-extensions

Secure credential brokering for [shed](https://github.com/charliek/shed) microVM development environments.

Credentials never enter the VM — all signing and secret resolution happens on the host, mediated by shed's plugin message bus. Standard tools (`git push`, AWS SDKs, `ssh`, `docker pull`) work without changes inside the VM.

## How It Works

```
┌─────────────────────────────────┐
│  shed microVM (Linux guest)     │
│                                 │
│  SSH client ──▶ shed-ext-ssh-agent        │
│  AWS SDK   ──▶ shed-ext-aws-credentials  │
│  Docker    ──▶ docker-credential-shed    │
│                    │            │
│              POST /v1/publish   │
│                    │            │
│              shed-agent (498)   │
└────────────────────┼────────────┘
                vsock│
┌────────────────────┼────────────┐
│  shed-server       │            │
│  plugin bus ───▶ SSE stream     │
└────────────────────┼────────────┘
                     │
┌────────────────────┼────────────┐
│  Host (macOS)      ▼            │
│  shed-host-agent                │
│    ├── SSH keys / agent             │
│    ├── AWS STS AssumeRole           │
│    ├── Docker credential helpers    │
│    ├── Touch ID gate (optional)     │
│    └── Audit log                    │
└─────────────────────────────────┘
```

## Credential Namespaces

| Namespace | Status | Description |
|-----------|--------|-------------|
| `ssh-agent` | Implemented | SSH key operations for git, SCP, remote access |
| `aws-credentials` | Implemented | AWS SDK credential vending via STS role assumption |
| `docker-credentials` | Implemented | Docker registry credential brokering for container pulls |

## Quick Start

### Host Setup

#### Homebrew (macOS, Recommended)

```bash
brew install charliek/tap/shed-host-agent
```

This installs the binary, generates a default config at `$(brew --prefix)/etc/shed/extensions.yaml`, and sets up a launchd service. Edit the config, then start:

```bash
brew services start shed-host-agent
```

#### Manual Install

1. Download the latest `shed-host-agent` from [Releases](https://github.com/charliek/shed-extensions/releases)

2. Create a config file:

    ```bash
    mkdir -p ~/.config/shed
    cat > ~/.config/shed/extensions.yaml << 'EOF'
    server: http://localhost:8080

    ssh: {}

    aws:
      source_profile: default
      default_role: arn:aws:iam::123456789012:role/your-dev-role

    docker:
      registries:
        - us-docker.pkg.dev
        - ghcr.io

    logging:
      enabled: true
    EOF
    ```

3. Start the host agent:

    ```bash
    shed-host-agent --config ~/.config/shed/extensions.yaml
    ```

### Guest

Use the extensions-enabled shed base image — guest-side binaries and systemd units are pre-installed. No configuration needed.

### Verify

From inside a shed:

```bash
# SSH — sign with your host key
ssh -T git@github.com

# AWS — get temporary credentials via STS
aws sts get-caller-identity

# Docker — pull from private registry
docker pull us-docker.pkg.dev/your-project/your-repo/image:tag

# Check extension health (from host)
shed list -vv
```

## shed-machine-rc (RC sessions on a native machine)

`shed-machine-rc` is the host-side sibling of the guest `shed-ext-rc` helper: the same RC
Session Convention engine, installed on a **native machine** (laptop, workstation, tailnet
host) so shed-remote-agent (and, later, shed-mobile) can bootstrap and watch
`claude remote-control` sessions on hosts that aren't sheds. It needs `claude` and `tmux`
on the machine.

```bash
# macOS
brew install charliek/tap/shed-machine-rc
# Linux — after adding the apt.stridelabs.ai repo (see github.com/charliek/apt-charliek)
sudo apt install shed-machine-rc

# Start a local auto-mode session, print its claude.ai URL, and walk away:
shed-machine-rc claude
```

Full reference: [shed-machine-rc](https://charliek.github.io/shed-extensions/reference/shed-machine-rc/).

## Security Properties

- SSH private keys never enter the VM — only signatures cross the bus
- AWS long-lived credentials never leave the host
- AWS STS tokens are short-lived (1 hour) and role-scoped per shed; an opt-in passthrough mode vends existing SSO/SAML session creds for setups with no assumable role
- Docker registry credentials brokered on demand with configurable registry allowlist
- Optional Touch ID approval gate for SSH sign operations
- All credential operations logged to host-side audit log

## Configuration

Host-side config at `~/.config/shed/extensions.yaml`:

```yaml
server: http://localhost:8080

ssh:
  # mode: agent-forward | local-keys | "" (auto-detect)
  approval:
    enabled: false

aws:
  source_profile: default
  default_role: arn:aws:iam::123456789012:role/dev
  session_duration: 1h
  cache_refresh_before: 5m

  # Per-server / per-shed role and mode overrides
  servers:
    mini2:
      sheds:
        my-service:
          role: arn:aws:iam::123456789012:role/dev
        sso-app:
          mode: passthrough   # SSO/SAML: vend source_profile session creds directly

docker:
  registries:
    - us-docker.pkg.dev
    - ghcr.io
  # allow_all: true

logging:
  enabled: true
  path: ~/.local/share/shed/extensions-audit.log
```

The SSH backend is auto-detected: if `SSH_AUTH_SOCK` exists on your Mac, it proxies to your existing agent (Secretive, 1Password, ssh-agent, etc.). Otherwise it reads keys from `~/.ssh/`.

## Development

```bash
mise install            # install Go 1.24 and golangci-lint
make check              # lint + test
make build-host         # build shed-host-agent (macOS)
make build-machine-rc   # build shed-machine-rc (host RC CLI)
make build-guest        # cross-compile guest binaries (Linux amd64 + arm64)
make docs-serve         # serve docs at http://127.0.0.1:7071
```

## Documentation

Full docs at [charliek.github.io/shed-extensions](https://charliek.github.io/shed-extensions/).

## License

See [LICENSE](LICENSE) for details.
