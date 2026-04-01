# Status CLI

`shed-ext status` is an in-VM health check tool that reports the connectivity status of each credential namespace.

## Usage

```bash
shed-ext status
```

## Output

### All Connected

```
ssh-agent:       ✓ connected (agent-forward mode, 3 keys available)
aws-credentials: ✓ connected (role: arn:aws:iam::123:role/dev, cached until 15:45 UTC)
```

### Partially Connected

```
ssh-agent:       ✓ connected (local-keys mode, 1 key available)
aws-credentials: ✗ not connected (shed-host-agent not responding)

Hint: start shed-host-agent on your Mac:
  shed-host-agent --config ~/.config/shed/extensions.yaml
```

### Not Connected

```
ssh-agent:       ✗ not connected (shed-host-agent not responding)
aws-credentials: ✗ not connected (shed-host-agent not responding)

Hint: start shed-host-agent on your Mac:
  shed-host-agent --config ~/.config/shed/extensions.yaml
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | All namespaces connected |
| 1 | One or more namespaces not connected |
| 2 | Usage error |

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--publish-url` | `http://127.0.0.1:498/v1/publish` | shed-agent publish endpoint |

## Status Detail

The status command queries each namespace with a `status` operation (2-second timeout per namespace):

- **SSH agent**: Reports backend mode (`agent-forward` or `local-keys`) and number of available keys
- **AWS credentials**: Reports configured IAM role and credential cache expiration time
