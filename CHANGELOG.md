# Changelog

## v0.4.0

### Breaking Changes

- **Removed the deprecated global `aws.sheds` map.** Per-shed AWS role overrides
  now live only under `aws.servers.<server>.sheds.<shed>`. A config that still
  sets a top-level `aws.sheds` is now **rejected at startup** with a migration
  message — rather than silently ignored, which could fall back to a broader
  `default_role` and over-grant. Migrate:

  ```yaml
  # before
  aws:
    sheds:
      web: { role: arn:aws:iam::123456789012:role/web }

  # after
  aws:
    servers:
      mini2:
        sheds:
          web: { role: arn:aws:iam::123456789012:role/web }
  ```

- **The shed-desktop approval channel is always-on at a fixed socket path** (#31).
  Removed the `desktop.enabled`, `desktop.socket_path`, and `desktop.timeout_ms`
  config keys — a config that still sets them loads with a deprecation warning
  (warn-and-ignore), it is not rejected. The per-approval budget moved to a new
  top-level `approval_timeout` (Go duration, default `25s`). Both Unix sockets
  (status + approval channel) now live at fixed, well-known paths instead of
  being YAML-configurable, overridable for tests / parallel agents via
  `SHED_HOST_AGENT_SOCKET_DIR`. shed-desktop needs no change — it already
  connects to that fixed path.

- **`shed-host-agent status` is now live-only** (#31). It queries the running
  daemon over its read-only status socket and prints the daemon's own
  self-report instead of re-reading a config file, so it can never disagree with
  the running service; it exits non-zero when the agent isn't running. The
  `status --live` flag is removed — plain `status` is always live.

### Features

- **Opt-in AWS `passthrough` mode for SSO/SAML** (#33) — a new `aws.mode`
  (`assume-role` default | `passthrough`) lets the agent vend the
  `source_profile`'s existing session credentials directly, skipping
  `sts:AssumeRole`, for environments where no assumable role exists (the real
  role is an instance/service role, or creds came from `AssumeRoleWithSAML`).
  `mode` layers per server/shed like `role`. Passthrough re-reads
  `~/.aws/credentials` on every request — so re-running your SSO login is picked
  up with no restart — and parses the `aws_session_expiration` /
  `x_security_token_expires` hint for accurate expiry, omitting `Expiration` when
  absent so the guest discovers expiry on a 403.

- **Audit failure code + reason** — a docker credential `get` that fails now
  records a machine-readable `code` (e.g. `REGISTRY_NOT_ALLOWED`,
  `APPROVAL_DENIED`) and a short host-side `reason` in the durable audit log and
  on the shed-desktop event stream, so *why* a pull failed is visible — not just
  that it did. Approval denials are distinguished from allowlist denials
  (`APPROVAL_DENIED` vs `REGISTRY_NOT_ALLOWED`) on host/admin surfaces, while the
  guest still receives `REGISTRY_NOT_ALLOWED` for both (behavior unchanged). The
  host-agent log lines gain a structured `code` attribute, and the shed-desktop
  approval prompt now names the registry being requested. New `AuditEntry`
  fields are additive (`omitempty`), so existing log parsers and older
  shed-desktop builds are unaffected.

- **`status` reports which config the running agent loaded** (#31, closes #29) —
  the absolute `config_path` appears in both the human and `--json` (schema 1)
  output, so a Homebrew user can confirm the service is using the expected file
  rather than a stale `~/.config` copy. The report also shows the effective
  policy per provider, whether a shed-desktop consumer is connected, and each
  watched server's per-namespace connection state.

### Docs

- **New [Host Agent IPC](docs/reference/host-agent-ipc.md) reference** documents
  the agent's two fixed-path Unix sockets — the read-only status socket
  (`LiveStatus` schema 1) and the approval channel (NDJSON handshake + frame
  catalog) — as a versioned public contract, so tooling beyond shed-desktop can
  build against them.

- **Allowlist Docker Hub as `index.docker.io`, not `docker.io`** — Docker
  requests Hub credentials for `https://index.docker.io/v1/`, which normalizes to
  `index.docker.io`; a bare `docker.io` entry never matches and pulls fail with
  `REGISTRY_NOT_ALLOWED`. Corrected the examples in
  `docs/reference/docker-credentials.md`, `docs/reference/configuration.md`, and
  `configs/extensions.example.yaml`.

## v0.3.7

### Fixes

- **`docker pull` of public images works again** (#28) — `docker-credential-shed`
  now speaks Docker's credential-helper protocol for the "no credential" case.
  When the host broker has no credential for an *allowed* registry, the guest
  emits `credentials not found in native keychain` on stdout (exit 1), so Docker
  falls back to an anonymous pull instead of aborting with
  `error getting credentials - err: exit status 1, out:`. The allowlist stays a
  **hard wall** — `REGISTRY_NOT_ALLOWED` aborts the pull even when a credential
  exists locally on the host — and genuine helper faults remain hard errors.

### Features

- **Audit no-credential pulls as `anonymous`** (#28) — a docker `get` for an
  allowed registry with no credential to serve is recorded with
  `result: anonymous` (info level) instead of `error`, so a public-image pull
  shows as a successful anonymous request rather than a red error in the audit
  log and the shed-desktop activity feed.

## v0.3.6

### Features

- **`shed-host-agent status`** — a host-side diagnostic (#23): effective approval
  policies, the desktop-socket state (`listening` / `missing` / `stale`), and
  per-server reachability + registered namespaces. `--json` for scripting.
- **`shed-host-agent status --live`** (#27) — the running daemon's own
  per-(server, namespace) connection state (`connected` / `reconnecting` + the
  failure reason), served over a read-only status socket.
- **Operational log rotation** (#25) — a `-log-file` flag writes a size-capped,
  rotated log (10 MB × 5 backups, gzip) instead of the unbounded launchd-captured
  stderr log (which had grown to 21 MB). The brew service sets it.

### Fixes

- **The desktop approval socket no longer vanishes** (#24) — the agent now
  refuses to bind over a *live* socket instead of clobbering it, so a second or
  stray agent can't delete the running agent's socket and silently break the
  app's reconnection (it looked "up" but unreachable until a service kickstart).

### Dependencies

- Bump `shed/sdk` to v0.1.1 (#26): reconnect-log dedup (no more a `WARN` per
  retry — a persistently-down server used to flood the log) plus
  `HostClient.Status()`, which powers `status --live`.

## v0.3.5

### Breaking changes

- **Per-provider `approval.policy`.** Each credential extension (`ssh`, `aws`,
  `docker`) now takes a single required `approval.policy`: `deny-all` (default,
  fail-closed) | `approve-all` | `shed-desktop`, plus SSH-only native Touch ID
  `biometrics` | `biometrics-or-password`. This **replaces**
  `ssh.approval.{enabled,method,session_ttl}` and renames the old
  `ssh.approval.policy` (per-request/per-session/per-shed) to
  `ssh.approval.scope`. There is **no migration**: update `extensions.yaml`.
  An omitted/empty policy means `deny-all`, so AWS and Docker now need an
  explicit `approval.policy` (e.g. `approve-all`) to vend credentials.
  ([#22](https://github.com/charliek/shed-extensions/pull/22))

### Features

- **AWS + Docker approval gating.** AWS credential vending and Docker registry
  credential requests now pass through the same approval gate as SSH — including
  `shed-desktop` delegation (a live Allow/Deny decided in the app), failing
  closed when the app isn't connected. `hello_ack.gate_namespaces` advertises
  which extensions are delegated (desktop protocol bumped to v2).
- **Complete decision audit.** The durable audit log records `decided_by`,
  `scope`, and `ttl` for every gated operation — approved, denied, and
  backend-error paths — even when the decision is made in shed-desktop.

### Notes

- The shipped default `extensions.yaml` gates SSH with Touch ID
  (`biometrics-or-password`, 1h), brokers Docker Hub + GitHub registries
  (`approve-all`), and leaves AWS `deny-all` until a role is set; shed-desktop
  delegation is documented but off by default.

## v0.3.4

### Features

- **Multi-server `shed-host-agent`.** A single agent process can now broker
  credentials for many shed servers at once, replacing the
  one-process-per-server workaround. Add a `discovery:` block to watch servers
  discovered from shed's CLI config (`~/.shed/config.yaml`) — all of them, or a
  named subset (`discovery.servers: [mini2, mini3]`). New servers added with
  `shed server add` are picked up live (via fsnotify, with `poll`/`off`
  alternatives) without a restart. Single-server configs using `server:` are
  unchanged. ([#21](https://github.com/charliek/shed-extensions/pull/21))
- **Hierarchical AWS/Docker config.** The `aws:` and `docker:` blocks gain a
  per-server / per-shed override tree (`servers.<server>.sheds.<shed>`) layered
  over the top-level defaults. Per-shed lookups — the AWS credential cache and
  role resolution, the Docker allowlist, and the Touch ID `per-shed` approval
  gate — are now keyed by `server/shed`, so identical shed names on different
  servers no longer collide.
- **Server-tagged audit.** Audit log entries and shed-desktop events carry a
  `server` field (omitted in single-server mode) so the combined log
  distinguishes activity per server.
- **shed-desktop approval delegation (opt-in, default-off).** A new optional
  Unix-domain-socket channel lets `shed-host-agent` delegate SSH `sign` approval
  decisions to the [shed-desktop](https://github.com/charliek/shed-desktop)
  menu-bar app, and streams an all-namespace audit/event feed to it. Disabled by
  default (`desktop.enabled: false`) and fully inert when off — no socket, no
  goroutine, no changed code path. Only request *metadata* crosses the socket;
  key material never leaves the agent. `ssh-agent` `sign` is gated;
  `aws-credentials`/`docker-credentials` are stream-only. Fails closed (a deny)
  on no app connected, timeout, or disconnect. Enable with `desktop.enabled:
  true` and `ssh.approval.method: shed-desktop`; protocol documented in
  `docs/reference/desktop-approval.md`.
  ([#20](https://github.com/charliek/shed-extensions/pull/20))

### Release process

- Bumped CI/release actions onto Node 24 runtimes:
  `actions/create-github-app-token` v2 → v3
  ([#18](https://github.com/charliek/shed-extensions/pull/18)),
  `docker/setup-qemu-action` v3 → v4 and `docker/build-push-action` v6 → v7
  ([#16](https://github.com/charliek/shed-extensions/pull/16)). The release
  workflow's App-token step now authenticates via `client-id` instead of the
  deprecated `app-id`
  ([#19](https://github.com/charliek/shed-extensions/pull/19)).

## v0.3.3

### Release process

- **Adopt the `cc-plugins:release-workflows` convention.** The release
  pipeline now mints a `charliek-release-bot` GitHub App token at
  workflow time (scoped to `charliek/homebrew-tap` with
  `permission-contents: write`) and feeds it to GoReleaser via the
  existing `HOMEBREW_TAP_TOKEN` env var, instead of consuming a
  long-lived personal access token. GoReleaser is token-source-
  agnostic — same `brews:` config, different credential plumbing. The
  legacy `HOMEBREW_TAP_TOKEN` PAT secret has been deleted ([#17](https://github.com/charliek/shed-extensions/pull/17)).
- **Docker push intentionally unchanged.** The `docker` job continues
  to use `secrets.GITHUB_TOKEN` as `${{ github.actor }}` for the
  ghcr.io push. This is the canonical pattern for same-repo ghcr.io
  packages and needs no App token.
- **Branch protection ruleset on `main`** with the App + admin role in
  `bypass_actors` (so `/release-workflows:release`'s commit-and-tag
  push lands). Previously shed-extensions had no branch protection.
- **`scripts/release/update-version.sh`**: a no-op for this repo
  (version derives from the git tag at build time via GoReleaser
  ldflags and the docker `--build-arg VERSION`), but the wrapper is
  present so the convention's contract holds and the next maintainer
  finds an obvious entry point.
- **`RELEASING.md`**: per-repo release policy, secrets table,
  break-glass recovery runbooks for both GoReleaser and Docker push
  failures.
- **`.github/workflows/sanity-check-app.yml`**: new manual workflow
  that verifies the release-bot App can reach `charliek/shed-extensions`
  + `charliek/homebrew-tap` before a release tries to push. Catches
  App-install-on-target mistakes early. Both reach checks validated
  before this release.

No changes to `shed-host-agent` behavior.

## v0.3.2

### Fixes

- Docker credential helpers (e.g. `docker-credential-desktop`) now resolve when the host agent runs under launchd's bare PATH — notably when started via `brew services` ([#12](https://github.com/charliek/shed-extensions/issues/12)). The agent resolves helpers to an absolute path, searching `/usr/local/bin` and `/opt/homebrew/bin` in addition to the inherited PATH, and the Homebrew service now sets a sane `PATH` in its launchd plist.
- SSH Touch ID approval now works in clamshell mode (lid closed) and on Macs without a fingerprint sensor ([#13](https://github.com/charliek/shed-extensions/issues/13)). The new `ssh.approval.method` field defaults to `biometrics-or-password` (`LAPolicyDeviceOwnerAuthentication` — Touch ID, Apple Watch, or account password); set `method: biometrics` to require Touch ID only (the previous, strict behavior).

### Changes

- Audit log `approval` field now records the configured method (`biometrics-or-password` or `biometrics`) instead of the literal `touchid`. Log consumers that match on `touchid` should be updated.
- Homebrew is now the recommended install method: the tap publishes `shed-host-agent` automatically on release, installable via `brew install charliek/tap/shed-host-agent` and runnable with `brew services` ([#8](https://github.com/charliek/shed-extensions/issues/8), [#10](https://github.com/charliek/shed-extensions/issues/10)).

## v0.3.1

### Features

- Add Docker registry credential helper extension (`docker-credentials` namespace)
- Guest binary `docker-credential-shed` brokers Docker registry credentials from host to VM
- Host backend reads `~/.docker/config.json` and shells out to credential helpers (gcloud, osxkeychain, ecr-login, etc.)
- Configurable registry allowlist with `allow_all` option
- Read-only broker — store and erase operations are rejected
- Audit logging for all Docker credential operations

## v0.2.0

### Features

- SSH agent credential brokering — proxies SSH sign operations from guest VMs to host keys
- AWS credential brokering — vends short-lived STS credentials to guest VMs via container credential endpoint
- Guest-side status CLI (`shed-ext status`) for checking namespace connectivity
- Touch ID approval gate for SSH sign operations (macOS, opt-in)
- Host-side audit logging of all credential operations
- Docker image distribution for guest binaries (`ghcr.io/charliek/shed-extensions`)

### Fixes

- Run guest services as `shed` user to fix socket permissions
- Fix stale socket paths in documentation

### Infrastructure

- Multi-arch Docker image (linux/arm64 + linux/amd64) for guest binary distribution
- GoReleaser config for `shed-host-agent` (darwin + linux)
- CI release workflow publishes both GitHub Release and Docker image on git tag
- Prerelease-safe `:latest` tagging for Docker images
- MkDocs Material documentation site
