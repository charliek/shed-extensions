# shed-desktop Approval Channel

`shed-host-agent` can delegate SSH approval decisions to the
[shed-desktop](https://github.com/charliek/shed-desktop) menu-bar app over a
local Unix-domain socket, instead of prompting Touch ID inline. The same socket
also carries an all-namespace audit/event stream that the app renders as a live
activity feed.

This is **disabled by default**. With it off, none of this code path runs and
behavior is unchanged.

## Enabling

```yaml
desktop:
  enabled: true                     # start the local approval socket
  socket_path: ~/Library/Application Support/shed/host-agent.sock
  timeout_ms: 25000                 # per-request approval budget

ssh:
  approval:
    method: shed-desktop            # delegate SSH sign approvals to the app
```

Both are required: `desktop.enabled` starts the socket, and
`ssh.approval.method: shed-desktop` selects the delegating gate. If the method
is set but the channel is disabled, the agent fails closed (denies every SSH
sign) rather than silently falling back.

## Scope (v1)

- **Gated:** only `ssh-agent` `sign` requests block on an app decision.
- **Stream-only:** `aws-credentials` and `docker-credentials` operations are not
  gated, but their audit entries are still streamed to the app so the activity
  feed is complete.

## Transport

- A Unix-domain socket at `socket_path` (mode `0600`; owner-only).
- Newline-delimited JSON, one typed envelope per line.
- Single active consumer, **last-writer-wins**: a new `hello` supersedes the
  previous app connection (the old one receives `hello_ack {accepted:false}`).

## Wire protocol

| Direction | type | Fields |
|-----------|------|--------|
| app → agent | `hello` | `client{name,version,pid}`, `capabilities[]`, `replay_events` |
| agent → app | `hello_ack` | `agent{version,approval_method}`, `namespaces[]`, `gate_namespaces[]`, `request_timeout_ms`, `accepted` |
| agent → app | `approval_request` | `id`, `namespace`, `op`, `shed`, `detail`, `expires_at` |
| app → agent | `approval_response` | `request_id`, `decision` (approve\|deny), `decided_by` |
| agent → app | `event` | `kind`, `shed`, `ns`, `op`, `result`, `detail`, `approval` (audit superset, all namespaces) |
| agent → app | `ping` / app → agent `pong` | liveness |

`approval_request.detail` carries only metadata (the key type for SSH). The app
never sees key material or secrets.

## Fail-closed semantics

The delegated gate degrades to the same outcome as an unanswered Touch ID
prompt today — a **deny**:

| Condition | Outcome |
|-----------|---------|
| No app connected | deny immediately |
| App connected, no response within `timeout_ms` | deny |
| App disconnects mid-request | deny |
| Duplicate / unknown `request_id` response | ignored |
| `decision: approve` within budget | sign proceeds |

## Security model

The agent remains the sole holder of credentials; the app only ever handles
request metadata, exactly as in the existing
[security posture](../security-posture.md). The socket is owner-only and there is
no network listener — the trust boundary is the local user account.
