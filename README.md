# shed-extensions

Secure credential brokering for [shed](https://github.com/charliek/shed) microVM development environments.

Credentials never enter the VM — all signing and secret resolution happens on the host, mediated by shed's plugin message bus. Standard tools (`git push`, AWS SDKs, `ssh`) work without changes inside the VM.

## How It Works

```
┌─────────────────────────────────┐
│  shed microVM (Linux guest)     │
│                                 │
│  SSH client ──▶ shed-ssh-agent  │
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
│    ├── SSH keys / agent         │
│    ├── Touch ID gate (optional) │
│    └── Audit log                │
└─────────────────────────────────┘
```

## Credential Namespaces

| Namespace | Status | Description |
|-----------|--------|-------------|
| `ssh-agent` | Implemented | SSH key operations for git, SCP, remote access |
| `aws-credentials` | Planned | AWS SDK credential vending via STS role assumption |

## Quick Start

### Host Setup

1. Download the latest `shed-host-agent` from [Releases](https://github.com/charliek/shed-extensions/releases)

2. Create a config file:

    ```bash
    mkdir -p ~/.config/shed
    cat > ~/.config/shed/extensions.yaml << 'EOF'
    server: http://localhost:8080
    ssh: {}
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
ssh -T git@github.com
```

Your SSH key never enters the VM. The sign request routes through the message bus to your Mac, where `shed-host-agent` signs with your local key.

## Security Properties

- SSH private keys never enter the VM — only signatures cross the bus
- AWS long-lived credentials never leave the host (Phase 2)
- AWS STS tokens are short-lived and role-scoped (Phase 2)
- Optional Touch ID approval gate for sign operations
- All credential operations logged to host-side audit log

## Configuration

Host-side config at `~/.config/shed/extensions.yaml`:

```yaml
server: http://localhost:8080

ssh:
  # mode: agent-forward | local-keys | "" (auto-detect)
  approval:
    enabled: false
    # policy: per-session    # per-request | per-session | per-shed
    # session_ttl: 4h

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
make build-guest        # cross-compile shed-ssh-agent (Linux amd64 + arm64)
make docs-serve         # serve docs at http://127.0.0.1:7071
```

## Documentation

Full docs at [charliek.github.io/shed-extensions](https://charliek.github.io/shed-extensions/).

## License

See [LICENSE](LICENSE) for details.
