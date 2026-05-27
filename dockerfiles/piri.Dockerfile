# Smelt dev image for piri-pdp.
#
# Built from a parent context (the dir that holds smelt/, piri-pdp/, libforge/,
# delegator/, ...) so piri-pdp's go.mod replace directives can find their
# sibling targets at build time.
#
# Mirrors piri-pdp/Dockerfile but lays out /src/piri-pdp + /src/libforge +
# /src/delegator instead of just /src.

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build

ARG TARGETARCH
ARG TARGETOS=linux

WORKDIR /src

# Bring in the siblings that piri-pdp's go.mod `replace ../<name>` points at.
COPY libforge /src/libforge
COPY delegator /src/delegator
# Piri source.
COPY piri-pdp /src/piri-pdp

WORKDIR /src/piri-pdp

RUN go mod download

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w" \
    -o /app \
    ./cmd

# Runtime stage - alpine for wget healthchecks per RFC
FROM alpine:latest AS prod

USER nobody

COPY --from=build /app /usr/bin/piri

ENTRYPOINT ["/usr/bin/piri"]
