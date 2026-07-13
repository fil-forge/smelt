# Hilt (Tenant Management)

Hilt manages Fil One tenants and their S3-style access credentials, mapping
them to UCAN authorizations in the Forge network (see the
[Forge S3 tenant management RFC](https://github.com/fil-one/rfc/blob/main/rfcs/2026-06-forge-s3-tenant-management.md)). It exposes:

- The **Tenant API** — partner-key (bearer token) REST API used by Fil One to
  create tenants and access keys.
- A **UCAN RPC API** — used by Ingot (the S3 facade, not yet in this stack) for
  `/s3/request/authorize`, `/s3/bucket/*` and by admins for provider
  registration.

On tenant creation, hilt generates a `did:plc` tenant key, publishes its
genesis operation to the local [PLC directory](../plc/), and registers
the tenant as a customer with the upload service via `/customer/add`.

## Services

- **hilt** - Tenant management service (`ghcr.io/fil-forge/hilt:main`)
- **hilt-postgres** - PostgreSQL for hilt's tenant/provider/access-key stores
  (goose migrations run at hilt startup)
- **hilt-vault** - HashiCorp Vault in dev mode for tenant/access-key private
  keys (KV v2 at the `secret` mount)

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15110 | 80 | hilt | Tenant API + UCAN RPC (`POST /`), did:web doc |
| 15111 | 5432 | hilt-postgres | PostgreSQL |
| 15112 | 8200 | hilt-vault | Vault HTTP API |

## Configuration

All configuration is via `HILT_*` environment variables in `compose.yml`:

- Identity: `/keys/hilt.pem` wrapped as `did:web:hilt` (DID document served at
  `/.well-known/did.json`).
- Tenant API auth: `HILT_AUTH_PARTNER_KEY` — defaults to `dev-partner-key`,
  override with `HILT_PARTNER_KEY=... make up`. Local-dev pre-shared value
  only; never use a production key here.
- Storage: postgres via the `hilt-postgres` sidecar (dev-only `hilt:hilt`
  credentials; data persists in the `hilt-postgres-data` volume).
- Vault: HashiCorp Vault dev mode via `hilt-vault`, token auth with the
  dev root token (`HILT_VAULT_TOKEN`, default `dev-root-token` — local dev
  only). Keys survive hilt restarts, but dev-mode Vault stores in memory, so
  a vault container restart clears them.
- PLC directory: the local reference server at `http://plc:3000`.
- Upload service: `did:web:upload` at `http://upload:80`, presenting the
  `upload → hilt` `/customer/add` delegation from
  `../../generated/proofs/hilt-customer-add-proof.txt`.

## Provider Registration

`post_start.sh` runs on every container start and registers **ingot** as the
regional provider for **us-west-1** via `hilt client admin provider add`,
reading the DID from `/piri-keys/ingot.did` (emitted by `smelt generate`).
Ingot must be the registered provider because hilt only accepts `/s3/*`
invocations issued by the tenant's provider.
Registration is idempotent — when the record already exists in postgres the
"already registered" response is tolerated. The script fails the container if
registration fails for any other reason (mirroring
`systems/upload/post_start.sh`).

## Keys

- `../../generated/keys/hilt.pem` - Hilt service identity (Ed25519)

## Volumes

- `hilt-postgres-data` - Hilt's tenant/provider/access-key records

## Dependencies

- plc (service_healthy)
- upload (service_healthy)
- hilt-postgres (service_healthy)
- hilt-vault (service_healthy)

## Requirements and Notes

- **Image versions**: the flow needs hilt with did:web resolver support
  (hilt `02b2afc`, config `HILT_SERVER_INSECURE_DID_RESOLUTION` — set in
  `compose.yml`) and sprue with the `/customer/add` handler (sprue
  `1f27110`). If the published `:main` images predate these, build from
  source with `SMELT_WORKSPACE=1 make up`.
- **Stale upload-postgres volumes**: sprue renamed the `customer.account`
  column to `external_account` by editing its initial migration in place, so
  stacks whose `upload-postgres-data` volume predates sprue `1f27110` fail
  tenant creation with `column "external_account" ... does not exist`.
  Recreate the volume: `docker compose stop upload postgres && docker compose
  rm -f upload postgres && docker volume rm smelt_upload-postgres-data &&
  make up` (or `make clean && make up`).
- Bucket creation and S3 request authorization require Ingot, which is not in
  the stack yet.

## Smoke Test

```bash
curl -sf http://localhost:15110/health
curl -s http://localhost:15110/.well-known/did.json | jq .id   # "did:web:hilt"

# No bearer key -> 401
curl -si http://localhost:15110/tenants/smoke-1

# Create a tenant (201; publishes did:plc genesis to the PLC directory and
# registers the tenant as a sprue customer via /customer/add)
curl -si -X PUT http://localhost:15110/tenants/smoke-1 \
  -H "Authorization: Bearer dev-partner-key" \
  -H "Content-Type: application/json" \
  -d '{"region":"us-west-1"}'

# Create an S3 access key (201; returns the secret once)
curl -si -X POST http://localhost:15110/tenants/smoke-1/access-keys \
  -H "Authorization: Bearer dev-partner-key" \
  -H "Content-Type: application/json" \
  -d '{"name":"dev-key","permissions":["s3:GetObject","s3:PutObject"]}'

# Tenant teardown (disable → delete publishes a did:plc tombstone)
curl -si -X POST http://localhost:15110/tenants/smoke-1/status \
  -H "Authorization: Bearer dev-partner-key" \
  -H "Content-Type: application/json" -d '{"status":"disabled"}'
curl -si -X DELETE http://localhost:15110/tenants/smoke-1 \
  -H "Authorization: Bearer dev-partner-key"
```

## Used By

- Ingot (S3 facade) — planned, not yet part of the stack
