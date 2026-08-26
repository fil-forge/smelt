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
- **ingot-openbao** — the regional OpenBao that holds ingot's region KEK
  (`openbao/openbao:2.6`, server mode, raft storage on `ingot-openbao-data`),
  sealed by [central-openbao](../central-openbao/).
- **ingot-openbao-init** — one-shot: initializes `ingot-openbao`, then
  provisions the transit engine, the region KEK, and ingot's token
  (`openbao/init.sh`).

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15130 | 9000 | ingot | S3 API (path-style only) + `/health` |
| 15131 | 5432 | ingot-postgres | PostgreSQL |
| 15132 | 8200 | ingot-openbao | OpenBao HTTP API |

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
- Revocation service: `did:web:swarf` at `http://swarf:80` — ingot subscribes
  to [swarf](../swarf/)'s revocation firehose so hilt's access-key deletions
  clear the per-key authorization caches.
- Region key: `regionkey.provider: openbao` against `http://ingot-openbao:8200`,
  transit mount `transit`, key `region-kek`. The token is
  `INGOT_REGIONKEY_OPENBAO_TOKEN` in `compose.yml` (default
  `dev-ingot-region-token`, override with `INGOT_OPENBAO_TOKEN=... make up`).
  Ingot images that predate the openbao provider
  ([fil-forge/ingot#105](https://github.com/fil-forge/ingot/pull/105)) ignore
  the block and serve plaintext only.

## Region KEK (OpenBao)

Per the [regional security and key management RFC](https://github.com/fil-one/RFC/pull/21),
each blob's CEK is wrapped by a transit `aes256-gcm96` key created with
`derived=true` (the wrap is bound to the blob's space and digest) and
`exportable=false`, held in an OpenBao local to the region. `ingot-openbao`
is that server. It runs as a real `bao server` (`openbao/config.hcl`:
TCP listener, raft storage) rather than dev mode, so the KEK persists, and
it is sealed by [central-openbao](../central-openbao/) through
`seal "transit"`: at start it presents its seal token (`BAO_TOKEN`, from
`INGOT_OPENBAO_SEAL_TOKEN`, default `dev-ingot-openbao-seal-token`), central
unwraps its barrier key, and it unseals. With central unreachable or the
token revoked there, the server does not start. That is the RFC's
startup-kill lever; see the central-openbao README for the revoke and
reinstate commands.

`ingot-openbao-init` runs on every `make up`:

1. First boot: `bao operator init` with one recovery share. The share and
   the root token are written to the `ingot-openbao-init` volume (`/init`).
   Dev-only custody; nothing here is a production secret.
2. Once, for a volume created before the transit seal (Shamir-sealed, its
   unseal share on `/init`): `bao operator unseal -migrate` moves it to the
   transit seal and the share becomes the recovery share.
3. Every boot: enable `transit` if missing; create
   `transit/keys/region-kek` (`aes256-gcm96`, `derived=true`,
   `exportable=false`) if missing; write the `ingot-region-kek` policy
   (encrypt, decrypt, rewrap on that key and nothing else); recreate ingot's
   token with the fixed id above. The token carries OpenBao's default TTL
   (768h) and nothing renews it, so a stack left up for more than 32 days
   needs `make down && make up` to mint a fresh one.

An `ingot-openbao` container restart auto-unseals through central. `make
clean` removes the OpenBao volumes together (ingot's and central's), which
keeps recovery material and the storage it belongs to in step.

## Keys and Proofs

- `../../generated/keys/ingot.pem` - Ingot agent identity (Ed25519, did:key)
- `../../generated/proofs/hilt-ingot-s3-proof.txt` - hilt → ingot delegations
  for `/s3/request/authorize` and `/s3/bucket/{create,delete,info,list}`
  (issuer `did:web:hilt`, audience ingot's did:key, subject `did:web:hilt`)

Ingot's did:key is also what the `hilt-init` registrar registers as the
**us-west-1 provider** — hilt only accepts S3 RPC invocations issued by the
tenant's registered provider.

## Volumes

- `ingot-data` - LSM log segments, blob spool, token store (`/data`)
- `ingot-postgres-data` - registry/metadata
- `ingot-openbao-data` - OpenBao raft storage (region KEK, transit state)
- `ingot-openbao-init` - unseal share + root token (dev-only custody)

## Dependencies

- hilt (service_healthy)
- upload (service_healthy)
- piri-0 (service_healthy)
- indexer (service_healthy)
- swarf (service_healthy)
- ingot-postgres (service_healthy)
- ingot-openbao-init (service_completed_successfully; itself waits on
  ingot-openbao service_healthy, which waits on central-openbao-init
  service_completed_successfully)

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

### Region KEK smoke test

```bash
# Server state (Seal Type transit, Sealed false)
docker compose exec ingot-openbao bao status

# Wrap and unwrap with ingot's scoped token, context-bound
CTX=$(printf 'did:key:zExample\0digest' | base64)
docker compose exec -e BAO_TOKEN=dev-ingot-region-token ingot-openbao \
  bao write -field=ciphertext transit/encrypt/region-kek \
  plaintext=$(printf 'thirty-two-byte-content-key-....' | base64) context=$CTX
# -> vault:v1:...; decrypt with the same context returns the plaintext,
#    a different context fails, and `bao read transit/keys/region-kek`
#    with this token is denied (403).
```

## Used By

- S3 clients (aws cli / SDKs, path-style addressing)
