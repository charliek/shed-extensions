# Multi-arch Dockerfile for shed-extensions guest artifact image.
#
# Produces a scratch-based image containing only:
#   /usr/local/bin/shed-ext-ssh-agent, /usr/local/bin/shed-ext-aws-credentials
#   /etc/systemd/system/shed-ext-{ssh-agent,aws-credentials}.service
#   /etc/shed-extensions.d/*.yaml (extension manifests)
#   /etc/environment.d/shed-extensions.conf
#
# Consumed by shed's VZ and Firecracker Dockerfiles via COPY --from=.
#
# Build locally (uses host arch — linux/arm64 on ARM Mac, linux/amd64 on x86 Linux):
#   docker buildx build --build-context shed-sdk=../shed/sdk -t ghcr.io/charliek/shed-extensions:dev --load .
#
# Build multi-arch:
#   docker buildx build --build-context shed-sdk=../shed/sdk --platform linux/arm64,linux/amd64 -t ghcr.io/charliek/shed-extensions:dev .
#
# Verify contents:
#   cid=$(docker create --entrypoint=/ ghcr.io/charliek/shed-extensions:dev)
#   docker export "$cid" | tar tf - | grep shed-
#   docker rm "$cid"

FROM --platform=$BUILDPLATFORM golang:1.24 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src

# Copy the shed SDK source for the replace directive (dev builds).
# Provide via: --build-context shed-sdk=../shed/sdk
COPY --from=shed-sdk . /shed-sdk
COPY go.mod go.sum ./
RUN sed -i 's|=> ../shed/sdk|=> /shed-sdk|' go.mod && go mod download

COPY . .
RUN sed -i 's|=> ../shed/sdk|=> /shed-sdk|' go.mod

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w \
        -X github.com/charliek/shed-extensions/internal/version.Version=${VERSION} \
        -X github.com/charliek/shed-extensions/internal/version.GitCommit=${GIT_COMMIT} \
        -X github.com/charliek/shed-extensions/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/shed-ext-ssh-agent ./cmd/shed-ext-ssh-agent && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w \
        -X github.com/charliek/shed-extensions/internal/version.Version=${VERSION} \
        -X github.com/charliek/shed-extensions/internal/version.GitCommit=${GIT_COMMIT} \
        -X github.com/charliek/shed-extensions/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/shed-ext-aws-credentials ./cmd/shed-ext-aws-credentials

FROM scratch

LABEL org.opencontainers.image.source="https://github.com/charliek/shed-extensions"
LABEL org.opencontainers.image.description="shed-extensions guest binaries and config for VM images"

COPY --from=builder /out/ /usr/local/bin/
COPY image/etc/ /etc/
