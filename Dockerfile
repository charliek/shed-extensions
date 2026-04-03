# Multi-arch Dockerfile for shed-extensions guest artifact image.
#
# Produces a scratch-based image containing only:
#   /usr/local/bin/shed-ssh-agent, /usr/local/bin/shed-aws-proxy, /usr/local/bin/shed-ext
#   /etc/systemd/system/shed-{ssh-agent,aws-proxy}.service
#   /etc/environment.d/shed-extensions.conf
#
# Consumed by shed's VZ and Firecracker Dockerfiles via COPY --from=.
#
# Build locally:
#   docker buildx build --platform linux/arm64 -t ghcr.io/charliek/shed-extensions:dev --load .
#   docker buildx build --platform linux/arm64,linux/amd64 -t ghcr.io/charliek/shed-extensions:dev .
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

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w \
        -X github.com/charliek/shed-extensions/internal/version.Version=${VERSION} \
        -X github.com/charliek/shed-extensions/internal/version.GitCommit=${GIT_COMMIT} \
        -X github.com/charliek/shed-extensions/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/shed-ssh-agent ./cmd/shed-ssh-agent && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w \
        -X github.com/charliek/shed-extensions/internal/version.Version=${VERSION} \
        -X github.com/charliek/shed-extensions/internal/version.GitCommit=${GIT_COMMIT} \
        -X github.com/charliek/shed-extensions/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/shed-aws-proxy ./cmd/shed-aws-proxy && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w \
        -X github.com/charliek/shed-extensions/internal/version.Version=${VERSION} \
        -X github.com/charliek/shed-extensions/internal/version.GitCommit=${GIT_COMMIT} \
        -X github.com/charliek/shed-extensions/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/shed-ext ./cmd/shed-ext

FROM scratch

LABEL org.opencontainers.image.source="https://github.com/charliek/shed-extensions"
LABEL org.opencontainers.image.description="shed-extensions guest binaries and config for VM images"

COPY --from=builder /out/ /usr/local/bin/
COPY image/etc/ /etc/
