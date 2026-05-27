# Smelt dev image for sprue (upload service).
#
# Built from a parent context (the dir that holds smelt/, sprue/, libforge/, ...)
# so sprue's go.mod `replace ../libforge` can find its sibling at build time.
# Mirrors the upstream sprue/Dockerfile but lays out /go/src/sprue + /go/src/libforge
# instead of just /go/src/sprue.

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETARCH
ARG TARGETOS=linux

# Place sprue at the same /go/src/sprue path the upstream Dockerfile uses so
# IDE remote source mapping (per smelt's CLAUDE.md debug-upload notes) still
# works without configuration changes.
WORKDIR /go/src/sprue

# Bring in libforge as a sibling on the same level go.mod's `replace ../libforge` expects.
COPY libforge /go/src/libforge

# Then sprue's own files.
COPY sprue/go.mod sprue/go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download || true
COPY sprue/ ./

# Production build - stripped binary
FROM build AS build-prod
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /sprue ./cmd/main.go

# Production runtime
FROM debian:bookworm-slim AS prod
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build-prod /sprue /usr/bin/sprue
EXPOSE 8080
ENTRYPOINT ["/usr/bin/sprue", "serve"]
