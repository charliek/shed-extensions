# AWS Credentials

The `aws-credentials` namespace brokers AWS credential access between shed microVMs and the host machine. Long-lived AWS credentials never enter the VM — only short-lived STS session tokens cross the bus.

## How It Works

`shed-aws-proxy` runs inside the VM as a systemd service, listening on `http://127.0.0.1:499`. The `AWS_CONTAINER_CREDENTIALS_FULL_URI` environment variable points all AWS SDKs to this endpoint.

When an AWS SDK requests credentials, `shed-aws-proxy` translates the request into a message bus call to the host agent. The host agent performs `sts:AssumeRole` using the developer's local AWS profile and returns temporary credentials.

```mermaid
sequenceDiagram
    participant SDK as AWS SDK (any language)
    participant Proxy as shed-aws-proxy (VM)
    participant Bus as Message Bus
    participant Host as shed-host-agent (Mac)
    participant STS as AWS STS

    SDK->>Proxy: GET /credentials
    Proxy->>Bus: publish(aws-credentials, get_credentials)
    Bus->>Host: SSE event
    Host->>Host: Check credential cache
    alt Cache valid
        Host->>Bus: cached credentials
    else Cache stale
        Host->>STS: sts:AssumeRole
        STS->>Host: temporary credentials
        Host->>Host: Update cache
        Host->>Bus: fresh credentials
    end
    Bus->>Proxy: response
    Proxy->>SDK: {AccessKeyId, SecretAccessKey, Token, Expiration}
```

## AWS SDK Integration

The AWS SDK credential chain checks `AWS_CONTAINER_CREDENTIALS_FULL_URI` automatically across all languages (Java, Python, Go, Node, Kotlin). No application code changes needed.

The proxy serves credentials in the exact format the SDK expects:

```
GET http://127.0.0.1:499/credentials
```

```json
{
  "AccessKeyId": "ASIA...",
  "SecretAccessKey": "...",
  "Token": "...",
  "Expiration": "2026-03-31T19:00:00Z"
}
```

The SDK handles refresh automatically — when credentials approach expiration, it re-requests from the endpoint.

## Role Configuration

Each shed maps to a specific IAM role via host-side configuration. The VM doesn't get to choose which role it receives.

```yaml
aws:
  source_profile: default
  default_role: arn:aws:iam::123456789012:role/smartthings-dev

  sheds:
    my-service:
      role: arn:aws:iam::123456789012:role/smartthings-dev
    integration-tests:
      role: arn:aws:iam::123456789012:role/smartthings-staging-readonly
```

## Message Format

### Request

```json
{
  "id": "0192b3a5-...",
  "namespace": "aws-credentials",
  "type": "request",
  "payload": {
    "operation": "get_credentials"
  }
}
```

### Response

```json
{
  "id": "0192b3a5-...",
  "namespace": "aws-credentials",
  "type": "response",
  "payload": {
    "access_key_id": "ASIAIOSFODNN7EXAMPLE",
    "secret_access_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
    "session_token": "FwoGZXIvYXdzE...",
    "expiration": "2026-03-31T19:00:00Z"
  }
}
```

### Error

```json
{
  "id": "0192b3a5-...",
  "namespace": "aws-credentials",
  "type": "response",
  "payload": {
    "error": "sts:AssumeRole failed",
    "code": "ASSUME_ROLE_FAILED"
  }
}
```

## Credential Caching

The guest-side proxy does not cache credentials — every SDK request passes through to the host. The host handler maintains a per-shed credential cache:

- Credentials are cached until they have less than 5 minutes remaining
- When the cache is stale, a fresh `sts:AssumeRole` call is made
- The AWS SDK's own refresh timing (~once per hour) drives re-fetching

Cache parameters are configurable:

```yaml
aws:
  session_duration: 1h         # STS token lifetime
  cache_refresh_before: 5m     # refresh when < 5 min remaining
```

## STS Session Details

- **Session name format**: `shed-{shed-name}-{timestamp}` for CloudTrail traceability
- **Default duration**: 1 hour (configurable)
- **Source credentials**: Loaded from `~/.aws/credentials` using the configured profile

## Timeouts

Credential requests use a 3-second timeout. On timeout, the proxy returns:

```json
{
  "error": "credential request timed out",
  "message": "shed-host-agent not reachable. Is it running on your Mac?",
  "hint": "Start it with: shed-host-agent --config ~/.config/shed/extensions.yaml"
}
```

## Startup Health Check

On startup, `shed-aws-proxy` pings the `aws-credentials` namespace. If no response arrives within 2 seconds, it logs a warning but continues starting.
