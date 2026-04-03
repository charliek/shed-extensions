# Quick Start

## Prerequisites

- A running [shed](https://github.com/charliek/shed) installation with shed-server
- macOS host (Apple Silicon or Intel)
- SSH keys configured on your Mac (via ssh-agent, Secretive, 1Password, etc.)
- For AWS: credentials configured in `~/.aws/credentials` and an IAM role to assume

## Host Setup

1. Download the latest `shed-host-agent` from [Releases](https://github.com/charliek/shed-extensions/releases):

    ```bash
    # Apple Silicon
    curl -L https://github.com/charliek/shed-extensions/releases/latest/download/shed-host-agent_darwin_arm64.tar.gz | tar xz
    sudo mv shed-host-agent /usr/local/bin/
    ```

2. Create a config file:

    ```bash
    mkdir -p ~/.config/shed
    cat > ~/.config/shed/extensions.yaml << 'EOF'
    server: http://localhost:8080

    ssh: {}

    aws:
      source_profile: default
      default_role: arn:aws:iam::123456789012:role/your-dev-role

    logging:
      enabled: true
    EOF
    ```

3. Start the host agent:

    ```bash
    shed-host-agent --config ~/.config/shed/extensions.yaml
    ```

## Guest Setup

Create a shed using the `experimental` image variant. The guest-side binaries and systemd units are pre-installed — no configuration needed:

```bash
shed create mydev --image experimental
```

See the [shed image variants documentation](https://charliek.github.io/shed/reference/images/) for details on selecting and building variants.

The `experimental` image includes:

- `shed-ssh-agent` — SSH agent proxy on `/run/shed-extensions/ssh-agent.sock`
- `shed-aws-proxy` — AWS credential endpoint on `http://127.0.0.1:499`
- `shed-ext` — CLI for checking namespace connectivity and health
- Environment variables `SSH_AUTH_SOCK` and `AWS_CONTAINER_CREDENTIALS_FULL_URI` pre-configured

## Verify SSH

From inside a shed:

```bash
ssh -T git@github.com
```

Your private key never enters the VM — the sign request routes through the bus to your Mac.

## Verify AWS

From inside a shed:

```bash
aws sts get-caller-identity
```

You should see the assumed role identity. No AWS credentials exist in the VM — the SDK fetches temporary credentials through the proxy.

## Per-Shed Role Overrides

Different sheds can assume different IAM roles:

```yaml
aws:
  source_profile: default
  default_role: arn:aws:iam::123456789012:role/dev

  sheds:
    my-service:
      role: arn:aws:iam::123456789012:role/dev
    integration-tests:
      role: arn:aws:iam::123456789012:role/staging-readonly
```

## What Happens

### SSH Flow

1. `git push` inside the shed triggers an SSH sign request
2. `shed-ssh-agent` sends the request through the message bus
3. `shed-host-agent` signs with your local SSH key
4. The signature flows back — git push succeeds

### AWS Flow

1. AWS SDK calls `GET http://127.0.0.1:499/credentials`
2. `shed-aws-proxy` sends the request through the message bus
3. `shed-host-agent` calls `sts:AssumeRole` (or returns cached credentials)
4. Temporary credentials flow back — SDK call succeeds
5. Credentials expire in 1 hour; SDK handles automatic refresh
