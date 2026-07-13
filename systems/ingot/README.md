# Ingot

Embeddable S3 gateway over the Forge network, run as a daemon. Presents an
S3 REST endpoint; splits each object into a **data-plane** CAR (object
bodies) and a **catalog-plane** CAR (MST nodes + manifests), and ships each
plane independently to Forge through the upload service (sprue) as a
guppy-style edge client.

## Services

- **ingot** — the S3 gateway daemon (forge mode).
- **ingot-postgres** — Postgres for ingot's registry/segment metadata
  (own `ingot` schema; ingot runs its own goose migrations on startup).

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15110 | 9000 | ingot | S3 REST endpoint |
| 15111 | 5432 | ingot-postgres | Postgres |

## Configuration

- `config/ingot.yaml` — daemon config (mode, S3 IAM, postgres DSN, sprue +
  indexer endpoints/DIDs). Override with `INGOT_*` env vars.

## Keys

- `../../generated/keys/ingot.pem` — the agent (service) identity that
  issues invocations to sprue. Add `"ingot"` to `nonPiriServiceKeys` in
  `pkg/generate/keys.go`, then `make regen`.

The **space** key (the DID all data is associated with) is auto-created at
`/data/space.key` on first run.

## Volumes

- `ingot-data` — local segment CARs, the space key, and `tokens.cbor`.
- `ingot-postgres-data` — Postgres data.

## Dependencies

- upload (service_healthy)
- piri-0 (service_healthy)
- indexer (service_healthy)
- ingot-postgres (service_healthy)

## Authorizing a space (required before uploads ship)

Forge mode needs ingot authorized to write to a space on sprue. ingot
self-issues space→agent delegations for the single-operator case, but
sprue must recognize the space. Run once (delegations persist in the
`ingot-data` volume):

```bash
docker compose exec ingot ingot -c /etc/ingot/config.yaml login <email>
```

then confirm via the emailed link (smelt's email-clicker harness, as used
for the SDK tests). Until the space is authorized, S3 PUTs are acked
locally (bytes durable on disk) but the background ship to Forge will
fail and retry — see daemon logs.

## Used By

- S3 clients (aws-cli, S3 SDKs) at `http://localhost:15110`.

## Status

This system is a deployment spec. The standalone (no-Forge) mode is fully
exercised by ingot's test suite; the forge-mode end-to-end ship depends on
the space-authorization step above and a published `ghcr.io/fil-forge/ingot`
image.
