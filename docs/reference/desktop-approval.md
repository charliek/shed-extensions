# shed-desktop Approval Channel

`shed-host-agent` can delegate approval decisions to the
[shed-desktop](https://github.com/charliek/shed-desktop) menu-bar app over a
local Unix-domain socket, instead of approving inline. The same socket also
carries an all-namespace audit/event stream that the app renders as a live
activity feed.

This is **disabled by default**. With it off, none of this code path runs.

## Enabling

Set `desktop.enabled: true` and the `approval.policy` of each extension you want
the app to decide to `shed-desktop`:

```yaml
desktop:
  enabled: true                     # start the local approval socket
  socket_path: ~/Library/Application Support/shed/host-agent.sock
  timeout_ms: 25000                 # per-request approval budget

ssh:
  approval:
    policy: shed-desktop            # decide SSH sign approvals in the app
aws:
  approval:
    policy: shed-desktop            # optional: toggle Allow/Deny in the app
docker:
  approval:
    policy: shed-desktop            # optional: toggle Allow/Deny in the app
```

`desktop.enabled` starts the socket; a `shed-desktop` policy selects the
delegating gate for that extension. If a policy is `shed-desktop` but the
channel is disabled (or the app isn't connected), the agent **fails closed** —
it denies rather than silently falling back.

## Scope

- **Gated:** any extension whose policy is `shed-desktop`. The agent advertises
  these in `hello_ack.gate_namespaces` so the app shows the matching approval
  UI. SSH presents the full interactive card (scope / TTL / always-allow /
  always-deny); AWS and Docker present a live Allow/Deny toggle.
- **Stream-only:** every other namespace's audit entries are still streamed to
  the app so the activity feed is complete, even when that extension is gated
  natively (Touch ID) or set to approve-all/deny-all.

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
| app → agent | `approval_response` | `request_id`, `decision` (approve\|deny), `decided_by`, `scope`, `ttl` |
| agent → app | `event` | `kind`, `shed`, `ns`, `op`, `result`, `detail`, `approval`, `decided_by`, `scope`, `ttl` (audit superset, all namespaces) |
| agent → app | `ping` / app → agent `pong` | liveness |

`approval_request.detail` carries only metadata (the key type for SSH). The app
never sees key material or secrets.

When the app approves, it returns `decided_by` (`user`/`touchid`/`policy`),
`scope`, and `ttl`; the agent records these in its **durable audit log**, so the
host-side record is complete regardless of where the decision was made.

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
