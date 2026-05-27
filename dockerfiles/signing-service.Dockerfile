# Smelt dev image for piri-signing-service.
#
# Parent context build so piri-signing-service's go.mod `replace ../libforge`
# resolves at build time. Mirrors the upstream Dockerfile but lays out
# /src/piri-signing-service + /src/libforge instead of just /src.

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build

ARG TARGETARCH
ARG TARGETOS=linux

WORKDIR /src

COPY libforge /src/libforge
COPY piri-signing-service /src/piri-signing-service

WORKDIR /src/piri-signing-service

RUN go mod download

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /app github.com/fil-forge/piri-signing-service

FROM alpine:latest AS prod

USER nobody

COPY --from=build /app /usr/bin/signer

EXPOSE 7446

ENTRYPOINT ["/usr/bin/signer"]
CMD ["--host", "0.0.0.0", "--port", "7446"]
