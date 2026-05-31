# Releasing shed-extensions

The general release framework is `cc-plugins:release-workflows`; this
file documents what's specific to this repo.

## TL;DR

    /release-workflows:release v0.3.3

That's it. Everything else is automatic.

## What happens

1. **`release-workflows:release`** (LLM, local):
   - Verifies branch (`main`) + clean tree + CI green on HEAD
     (shed-extensions has no `ci-success` aggregator; the skill reports
     "missing" for that check, which is expected — treat as
     non-blocking; the `release` job's inline `go test` + lint serve
     as the real gate)
   - Asks/confirms version
   - Drafts a CHANGELOG entry from `git log v<previous>..HEAD`, commits
     as `docs(changelog): vX.Y.Z entry`
   - Runs `scripts/release/update-version.sh X.Y.Z` — **a no-op for
     shed-extensions** because Go's version comes from build-time
     ldflags + the docker job's `VERSION` build-arg, not a source-tree
     manifest. The commit will be content-empty but tags reliably on a
     known commit.
   - Commits as `chore(version): bump to X.Y.Z`
   - Tags `vX.Y.Z` (annotated) on the version commit
   - `git push --follow-tags` (admin bypasses the ruleset)

2. **`release.yaml`** (CI, on tag push `v*`) — two jobs:

   **`release` job** (runs on `macos-latest` because shed-host-agent
   needs CGO for Touch ID / biometric APIs on darwin):
   - Checks out, sets up Go, runs `go test ./...` + golangci-lint
   - Mints a release-bot App token scoped to `charliek/homebrew-tap`
   - Runs `goreleaser release --clean`:
     - Builds `shed-host-agent` for `darwin/{amd64,arm64}` (CGO on) and
       `linux/{amd64,arm64}` (CGO off) with version + GitCommit +
       BuildDate ldflags
     - Tarballs as `shed-host-agent_<os>_<arch>.tar.gz` with
       `configs/extensions.example.yaml` bundled at the archive root
     - Uploads tarballs + `checksums.txt` to the GitHub Release
     - Auto-generates release notes from commits (filtered: skip
       `docs:`, `test:`, `chore:`, `ci:`)
     - Pushes `Formula/shed-host-agent.rb` to `charliek/homebrew-tap`
       using the App-minted token (replaces the legacy
       `HOMEBREW_TAP_TOKEN` PAT)

   **`docker` job** (runs on `ubuntu-latest`, `needs: release`):
   - Computes image tags (`vX.Y.Z`, plus `latest` for stable releases)
   - Sets up QEMU + Docker Buildx
   - Logs in to `ghcr.io` as `${{ github.actor }}` using
     `secrets.GITHUB_TOKEN` (canonical pattern for ghcr.io packages
     owned by this same repo — NOT App-based, intentionally; see
     "Notes for this repo" below)
   - Builds and pushes `ghcr.io/charliek/shed-extensions:<tags>` for
     `linux/{amd64,arm64}` with `VERSION` / `GIT_COMMIT` / `BUILD_DATE`
     build-args

The maintainer runs step 1; everything else is automated.

## Version files this repo owns

`scripts/release/update-version.sh` bumps nothing. shed-extensions's
canonical version is the git tag, injected at build time via:

```
-X github.com/charliek/shed-extensions/internal/version.Version={{.Version}}
-X github.com/charliek/shed-extensions/internal/version.GitCommit={{.ShortCommit}}
-X github.com/charliek/shed-extensions/internal/version.BuildDate={{.Date}}
```

in `.goreleaser.yaml`'s `builds[].ldflags`, plus the docker job's
`--build-arg VERSION=${GITHUB_REF_NAME}` for the container image.

`CHANGELOG.md` is maintained by the release skill for human-readable
in-repo history. GoReleaser's auto-generated release notes (from commit
subjects, filtered) go on the GitHub Release body separately.

## Snapshot / dev versioning

Not used. `shed-host-agent version` between releases shows the last
released version.

## Secrets

| Secret | Purpose | Required? |
|---|---|---|
| `RELEASE_BOT_APP_ID` | `charliek-release-bot` GitHub App ID | required — for the homebrew-tap push |
| `RELEASE_BOT_APP_KEY` | App private key (.pem) | required — same |
| `GITHUB_TOKEN` (workflow-provided) | Docker push to ghcr.io as `${{ github.actor }}` | provided automatically; do not set |

Retired (deleted from `gh secret list -R charliek/shed-extensions`
during the convention adoption):

- `HOMEBREW_TAP_TOKEN` — replaced by the App-minted homebrew-tap token.
  GoReleaser still reads the env var named `HOMEBREW_TAP_TOKEN`; the
  workflow sets it from `steps.tap.outputs.token` instead of from
  `secrets`. Confirm with `gh secret list` that only the
  `RELEASE_BOT_APP_*` pair remains.

## Branch protection

`main` is protected by a ruleset (created during the convention
adoption) with rules `deletion` + `non_fast_forward` (no
`required_status_checks` — shed-extensions's `ci.yml` runs separate
jobs with no aggregator). Bypass actors:

- `charliek-release-bot` (App, type `Integration`) — lets the App push
  any future post-build asset updates back to shed-extensions's main
  (no such step today; bypass-listed for future flexibility)
- Admin role (id `5`, type `RepositoryRole`) — lets
  `/release-workflows:release`'s push of the changelog + version
  commits + tag land

Inspect or edit at https://github.com/charliek/shed-extensions/rules.

## App installation

The release-bot App must be installed on two repos:

- `charliek/shed-extensions` itself (so the workflow's secrets resolve)
- `charliek/homebrew-tap` (so the minted token can push the formula)

The Docker push does NOT need the App on a separate repo because the
ghcr.io package is owned by this same repo and uses the workflow's
`GITHUB_TOKEN`.

Verify both via the `sanity-check-app.yml` workflow (Actions → Run
workflow). Each block must print the expected repo name.

## When things break

| Symptom | Cause | Fix |
|---|---|---|
| `git push` rejected | Pusher not in ruleset bypass | Confirm App + admin role in `main`'s ruleset `bypass_actors` |
| GoReleaser fails at `brews` with `Bad credentials` | `RELEASE_BOT_APP_ID` unset OR App not installed on `homebrew-tap` | Confirm via `sanity-check-app.yml`'s homebrew-tap block; install the App on the tap |
| `docker` job fails at "Log in to ghcr.io" | `packages: write` permission missing OR the actor doesn't have package-push perms | Confirm `permissions.packages: write` on the docker job |
| `release` job fails on macos-latest with build errors | Touch ID / CGO build broke | Fix on a branch, merge, cut a fresh patch tag |
| `brew install` finds old shed-host-agent | Tap cache | `brew untap charliek/tap && brew tap charliek/tap` |
| Pulled Docker image doesn't have new version | ghcr.io cache, or tag race | Verify `ghcr.io/charliek/shed-extensions:vX.Y.Z` exists; re-pull `:latest` |

## Break-glass recovery

### GoReleaser failed after some artifacts uploaded

```bash
RUN_ID=$(gh run list -R charliek/shed-extensions --workflow release.yaml \
                     --limit 1 --json databaseId --jq '.[0].databaseId')
gh run rerun "${RUN_ID}" -R charliek/shed-extensions --failed
```

GoReleaser's `mode: replace` reuses the existing Release.

### Docker push failed

Re-run the `docker` job from the Actions UI. It's independent of the
`release` job's GoReleaser step — the rerun only retries the Docker
build and push. (It does `needs: release`, so if you trigger it from
the UI as a "failed jobs" rerun it'll skip the satisfied dependency
and just retry the docker build.)

## Adopting the convention (for new contributors)

If you're new to this repo and need to understand the release pipeline,
read [`cc-plugins/plugins/release-workflows/references/convention.md`](https://github.com/charliek/cc-plugins/blob/main/plugins/release-workflows/references/convention.md)
in the framework repo.

## Notes for this repo

- **Docker push uses `GITHUB_TOKEN`, NOT the release-bot App**:
  ghcr.io packages owned by a repo accept pushes from that repo's
  workflow token as `${{ github.actor }}`. This is the canonical
  ghcr.io pattern. Migrating to an App token would mean setting up the
  App on a separate namespace, minting a scoped token, and managing
  package-write permissions in ghcr.io's UI — without solving any
  actual problem (the App's value is "single identity for *cross-repo*
  pushes", and this is a same-repo push). Intentionally unchanged.
- **No `version-check` step**: shed-extensions has no source-tree
  version manifest. Version comes from GoReleaser ldflag + docker
  build-arg at build time, so there's nothing to cross-check against
  the tag.
- **No `ci-gate` job**: shed-extensions's `ci.yml` runs separate jobs
  with no aggregator. The `release` job's inline `go test` + lint
  serve as the inline gate at tag time.
- **`release` job runs on `macos-latest`** because shed-host-agent's
  darwin build needs CGO (Touch ID / biometric APIs). The linux build
  cross-compiles from macOS with `CGO_ENABLED=0`.
- **No apt distribution**: shed-host-agent is a developer/operator
  tool shipped via Homebrew and Docker; no `.deb` packaging. No
  apt-charliek dispatch in `release.yaml`, no apt-charliek block in
  `sanity-check-app.yml`.
- **`pyproject.toml`** is for mkdocs only; has its own version cadence
  and is not touched by `update-version.sh`.
