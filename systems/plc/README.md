# PLC (did:plc Directory)

The reference [did:plc](https://github.com/did-method-plc/did-method-plc)
directory server (`@did-plc/server` — the same implementation behind the
public [plc.directory](https://plc.directory)). Hilt publishes tenant
`did:plc` identities (genesis operations, key rotations, tombstones) here,
keeping local test data off the public internet while still exercising real
validation:

- Operation signature verification against the DID's rotation keys
- `did:plc` derivation from the genesis operation (sha256 → base32 → 24 chars)
- `prev` CID chain integrity and tombstone rules

## Services

- **plc** - PLC directory server (Node)
- **plc-postgres** - PostgreSQL for the operation log (migrations run at
  startup)

## Image

`plc` pulls a published image rather than building from source:

```yaml
image: ${PLC_IMAGE:-ghcr.io/fil-forge/did-method-plc:main}
```

The image is published by the [fil-forge fork](https://github.com/fil-forge/did-method-plc)
of did-method-plc. Upstream's own ghcr workflow produced an unpullable image
(malformed tag, private package), so the fork carries the publish fix pending an
upstream PR. Override `PLC_IMAGE` (e.g. a specific SHA tag) to pin a different
build.

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15120 | 3000 | plc | PLC directory API |
| 15121 | 5432 | plc-postgres | PostgreSQL |

## Endpoints

- `POST /{did}` - Publish a signed `plc_operation` or `plc_tombstone`
- `GET /{did}` - Resolved DID document (404 once tombstoned)
- `GET /{did}/data` - Current DID data state
- `GET /{did}/log` / `GET /{did}/log/last` / `GET /{did}/log/audit` - Op log
- `GET /export` - Paginated JSONL export of all operations
- `GET /_health` - Health probe

## Configuration

Via environment in `compose.yml`: `DATABASE_URL` (dev credentials `plc:plc`,
local stack only), `LOG_ENABLED`, `LOG_LEVEL`. The server holds no keys.

## Volumes

- `plc-postgres-data` - PLC operation log (persists across restarts)

## Used By

- hilt (`HILT_PLC_DIRECTORY=http://plc:3000`)
