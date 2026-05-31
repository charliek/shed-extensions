#!/usr/bin/env bash
# Bump shed-extensions's release version.
#
# shed-extensions ships a Go binary (shed-host-agent) via Homebrew and a
# Docker image via ghcr.io. Both version surfaces come from the git tag
# at build time:
#
#   * shed-host-agent (Go) — GoReleaser injects
#       -X github.com/charliek/shed-extensions/internal/version.Version={{.Version}}
#       -X github.com/charliek/shed-extensions/internal/version.GitCommit={{.ShortCommit}}
#       -X github.com/charliek/shed-extensions/internal/version.BuildDate={{.Date}}
#     via .goreleaser.yaml's builds[].ldflags.
#
#   * Docker image — the `docker` job in release.yaml passes
#       --build-arg VERSION=${GITHUB_REF_NAME}
#     and tags as ghcr.io/charliek/shed-extensions:vX.Y.Z (+ :latest).
#
# There's no source-tree manifest to bump — this script is intentionally
# a no-op, present only so the cc-plugins:release-workflows convention's
# contract holds (the release skill checks for and runs it, and the
# version-derivation expectation is documented for the next maintainer).
#
# Adapted from the cc-plugins:release-workflows convention's "Special
# case — Go modules" guidance in setup.md Phase 4.

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <X.Y.Z>   e.g. $0 0.3.3" >&2
  exit 2
fi
V="$1"

if [[ ! "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]]; then
  echo "error: '$V' is not semver (X.Y.Z or X.Y.Z-suffix)" >&2
  exit 2
fi

echo "Go module + Docker image: version (${V}), GitCommit, and BuildDate" \
     "are injected at build time from the tag (GoReleaser ldflags +" \
     "docker --build-arg VERSION). Nothing to bump in the source tree."
