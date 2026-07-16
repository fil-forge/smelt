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
| 15130 | 9000 | ingot | S3 API (path-style only) + `/health` |
| 15131 | 5432 | ingot-postgres | PostgreSQL |

## Configuration

- `config/config.yaml` - mounted at `/etc/ingot/config.yaml` (ingot's default
  search path). Config must live in the file: ingot's viper env binding does
  not populate un-defaulted keys, so `INGOT_*` env vars only override keys
  already present in the file.
- Identity: `/keys/ingot.pem` (did:key — ingot serves no did:web document;
  hilt and sprue DIDs are configured verbatim, no resolution performed).
- Root S3 credentials: `ingot-root` / `ingot-root-secret` (dev-only values).
- Region: `us-west-1` — must match hilt's registered provider region and the
  sigv4 region S3 clients use.

## Keys and Proofs

- `../../generated/keys/ingot.pem` - Ingot agent identity (Ed25519, did:key)
- `../../generated/proofs/hilt-ingot-s3-proof.txt` - hilt → ingot delegations
  for `/s3/request/authorize` and `/s3/bucket/{create,delete,info,list}`
  (issuer `did:web:hilt`, audience ingot's did:key, subject `did:web:hilt`)

Ingot's did:key is also what hilt's post_start registers as the **us-west-1
provider** — hilt only accepts S3 RPC invocations issued by the tenant's
registered provider.

## Volumes

- `ingot-data` - LSM log segments, blob spool, token store (`/data`)
- `ingot-postgres-data` - registry/metadata

## Dependencies

- hilt (service_healthy)
- upload (service_healthy)
- ingot-postgres (service_healthy)

## Build Requirement

The image builds from the sibling checkout (`build.context: ../../../ingot`),
so `fil-forge/ingot` must be checked out next to smelt. Test stacks
(`pkg/stack`) extract compose files to a temp dir without the sibling, so
build the image once first (`make up` or `docker compose build ingot`) — the
`smelt-ingot:dev` tag is then reused.

## Smoke Test

```bash
# Tenant + access key via hilt (see systems/hilt/README.md), then:
export AWS_ACCESS_KEY_ID=<accessKeyId> AWS_SECRET_ACCESS_KEY=<secretAccessKey> AWS_REGION=us-west-1
aws configure set default.s3.addressing_style path
aws --endpoint-url http://localhost:15130 s3api create-bucket --bucket test-bucket
aws --endpoint-url http://localhost:15130 s3api put-object --bucket test-bucket --key hello.txt --body ./hello.txt
aws --endpoint-url http://localhost:15130 s3api get-object --bucket test-bucket --key hello.txt /tmp/hello.txt
aws --endpoint-url http://localhost:15130 s3api list-buckets
```

## Used By

- S3 clients (aws cli / SDKs, path-style addressing)
