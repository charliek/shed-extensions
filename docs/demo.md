# Demo Script

Walkthrough for demonstrating shed-extensions credential isolation to stakeholders.

## Prerequisites

- shed-server running on your Mac
- shed-host-agent configured and running
- A shed created with the extensions-enabled base image
- SSH key configured on your Mac (any agent: Secretive, 1Password, ssh-agent)
- AWS credentials configured in `~/.aws/credentials` with a role to assume
- For Docker: Docker credential helpers configured on your Mac and registries in the host config allowlist

## Setup

### Terminal 1: Host Agent (with audit log tailing)

```bash
# Start the host agent
shed-host-agent --config ~/.config/shed/extensions.yaml
```

### Terminal 2: Audit Log

```bash
# Watch credential operations in real-time
tail -f ~/.local/share/shed/extensions-audit.log | jq .
```

### Terminal 3: Inside the Shed

```bash
# Connect to your shed
shed attach my-service
```

## Demo Flow

### 1. Status Check

Show that both credential namespaces are connected:

```bash
shed-ext status
```

Expected output:
```text
ssh-agent:          ✓ connected (agent-forward mode, 3 keys available)
aws-credentials:    ✓ connected (role: arn:aws:iam::123:role/dev, cached until 15:45 UTC)
docker-credentials: ✓ connected (allow_all: false, 2 registries)
```

### 2. SSH — Git Push Without Keys

Show there are no SSH keys in the VM:

```bash
ls -la ~/.ssh/
# Empty or nonexistent
```

Now use SSH normally:

```bash
ssh -T git@github.com
# Hi username! You've successfully authenticated...
```

Point out: the private key never entered the VM. Check the audit log on the host — you'll see a `sign` entry.

### 3. AWS — SDK Access Without Credentials

Show there are no AWS credentials in the VM:

```bash
env | grep -i aws
# Only AWS_CONTAINER_CREDENTIALS_FULL_URI is set — no keys, no secrets

cat ~/.aws/credentials 2>/dev/null
# File doesn't exist
```

Now use AWS normally:

```bash
aws sts get-caller-identity
```

Expected output:
```json
{
    "UserId": "AROA...:shed-my-service-1711900800",
    "Account": "123456789012",
    "Arn": "arn:aws:sts::123456789012:assumed-role/dev/shed-my-service-1711900800"
}
```

Point out:
- The session name includes the shed name for CloudTrail traceability
- The credentials are temporary (1-hour STS tokens)
- Check the audit log — you'll see a `get_credentials` entry

### 4. Docker — Pull Without Login

Show there is no Docker login in the VM:

```bash
cat ~/.docker/config.json
# Only shows credsStore: shed — no inline credentials
```

Now pull from a private registry:

```bash
docker pull us-docker.pkg.dev/your-project/your-repo/your-image:tag
```

Point out: no `docker login` was needed. The credential helper resolved credentials from your Mac's Docker config. Check the audit log — you'll see a `get` entry for the `docker-credentials` namespace.

### 5. Security Verification

Demonstrate that credential material doesn't exist in the VM:

```bash
# No SSH private keys
find / -name "id_*" -not -name "*.pub" 2>/dev/null
# Empty

# No AWS credential files
find / -name "credentials" -path "*/.aws/*" 2>/dev/null
# Empty

# No credential environment variables
env | grep -E "(AWS_SECRET|AWS_ACCESS|SSH_PRIVATE)"
# Empty
```

### 6. Audit Log Review

Switch to the audit log terminal. Show the JSON entries:

```json
{"ts":"...","shed":"my-service","ns":"ssh-agent","op":"sign","result":"ok","detail":"ssh-ed25519","approval":"none"}
{"ts":"...","shed":"my-service","ns":"aws-credentials","op":"get_credentials","result":"ok","detail":"expires:16:04","approval":"none"}
{"ts":"...","shed":"my-service","ns":"docker-credentials","op":"get","result":"ok","detail":"us-docker.pkg.dev","approval":"none"}
```

Point out: every credential operation is logged with the shed name, operation type, and result.

## Key Talking Points

1. **Zero friction**: Standard tools work without any configuration inside the VM
2. **Zero credential exposure**: Private keys and long-lived secrets never enter the execution environment
3. **Audit trail**: Every credential operation is logged with context (which shed, what operation, when)
4. **Role scoping**: Each shed gets exactly one IAM role — least privilege by default
5. **Key management freedom**: Developers keep their existing tools (Secretive, 1Password, yubikey)
6. **Automatic refresh**: AWS SDKs handle token refresh transparently — no developer action needed
