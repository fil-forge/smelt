# Swarf (UCAN Revocation)

Swarf is the Forge network's UCAN revocation service. It accepts
`ucan/revoke` invocations over a UCAN RPC endpoint, stores revocation
records (revoked delegation CID + delegation path witness + cause
invocation) in Postgres, and serves them back over HTTP:

- `POST /` — UCAN RPC (`ucan/revoke`)
- `GET /revocation/:cid` — DAG-JSON revocation record (404 if absent)
- `GET /revocations/:since` — SSE firehose of revocation records
  (`0` or an RFC3339 timestamp)
- `GET /.well-known/did.json` — did:web document
- `GET /` — server info (also the healthcheck target; swarf has no
  `/health` route)

## Services

- **swarf** - Revocation service (`ghcr.io/fil-forge/swarf:main`)
- **swarf-postgres** - PostgreSQL for swarf's revocation store (goose
  migrations run at swarf startup)

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15140 | 80 | swarf | UCAN RPC (`POST /`), revocation reads, did:web doc |
| 15141 | 5432 | swarf-postgres | PostgreSQL |

## Configuration

All configuration is via `SWARF_*` environment variables in `compose.yml`:

- Identity: `/keys/swarf.pem` wrapped as `did:web:swarf` (DID document
  served at `/.well-known/did.json`).
- Storage: postgres via the `swarf-postgres` sidecar (dev-only
  `swarf:swarf` credentials; data persists in the `swarf-postgres-data`
  volume).
- `SWARF_SERVER_INSECURE_DID_RESOLUTION=true` so did:web documents
  resolve over plain HTTP inside the compose network.

## Keys

- `../../generated/keys/swarf.pem` - Swarf service identity (Ed25519)

## Volumes

- `swarf-postgres-data` - Swarf's revocation records

## Dependencies

- swarf-postgres (service_healthy)

## Smoke Test

```bash
curl -sf http://localhost:15140/                                # server info banner
curl -s http://localhost:15140/.well-known/did.json | jq .id    # "did:web:swarf"
curl -si http://localhost:15140/revocation/bafyreib3mqe6t2z3xwqcwoohw6f5o5t5nprfltmbjbmzynyqcnpxrcut4q  # 404
```

## Used By

- Hilt and Ingot — startup ordering is wired (`depends_on`), but neither
  is configured to call swarf yet.
