# Quick Start

## Prerequisites

- A running [shed](https://github.com/charliek/shed) installation with shed-server
- macOS host (Apple Silicon or Intel)
- SSH keys configured on your Mac (via ssh-agent, Secretive, 1Password, etc.)

## Host Setup

1. Download the latest `shed-host-agent` from [Releases](https://github.com/charliek/shed-extensions/releases):

    ```bash
    # Apple Silicon
    curl -L https://github.com/charliek/shed-extensions/releases/latest/download/shed-host-agent_darwin_arm64.tar.gz | tar xz
    sudo mv shed-host-agent /usr/local/bin/
    ```

2. Create a minimal config:

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

## Guest Setup

Use the extensions-enabled shed base image. The guest-side binaries and systemd units are pre-installed — no configuration needed.

## Verify

From inside a shed:

```bash
# SSH agent should be connected
ssh -T git@github.com

# Check extension status (Phase 3)
# shed-ext status
```

## What Happens

1. `git push` inside the shed triggers an SSH sign request
2. `shed-ssh-agent` (in the VM) sends the request through the message bus
3. `shed-host-agent` (on your Mac) signs with your local SSH key
4. The signature flows back — git push succeeds
5. Your private key never entered the VM
