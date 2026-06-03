# Changelog

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
