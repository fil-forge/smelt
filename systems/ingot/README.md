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
  (`openbao/openbao:2.6`, server mode, raft storage on `ingot-openbao-data`).
- **ingot-openbao-init** — one-shot: initializes and unseals `ingot-openbao`,
  then provisions the transit engine, the region KEK, and ingot's token
  (`openbao/init.sh`).

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15130 | 80 | ingot | S3 API (path-style only) + `/health` + `/.well-known/did.json` |
| 15131 | 5432 | ingot-postgres | PostgreSQL |
| 15132 | 8200 | ingot-openbao | OpenBao HTTP API |

## Configuration

- `config/config.yaml` - mounted at `/etc/ingot/config.yaml` (ingot's default
  search path). Config must live in the file: ingot's viper env binding does
  not populate un-defaulted keys, so `INGOT_*` env vars only override keys
  already present in the file.
- Identity: `did:web:ingot` (`identity.service_id`) wrapping the key in
  `/keys/ingot.pem`. Ingot serves the DID document at
  `http://ingot:80/.well-known/did.json`; hilt, sprue and piri resolve it
  there (over plain HTTP, their `insecure_did_resolution` dev setting) to
  verify ingot's invocations. Ingot itself resolves no DIDs: the hilt, sprue
  and swarf DIDs are configured verbatim.
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
- Tenant wrap key: `tenantkey.plc_directory_url: http://plc:3000`. On the
  write path ingot resolves the tenant's `did:plc` document from
  [plc](../plc/) and encrypts the object to the X25519 key hilt publishes at
  the tenant's `#wrap` verification method. Writes fail if the directory is
  unreachable or the document has no usable `#wrap` key. Resolved documents
  are cached for `tenantkey.cache_ttl` (default 10m), which is also how long
  a wrap-key rotation can go unseen. Ingot images that predate the tenant
  recipient
  ([fil-forge/ingot#112](https://github.com/fil-forge/ingot/pull/112)) ignore
  the block.

## Region KEK (OpenBao)

Per the [regional security and key management RFC](https://github.com/fil-one/RFC/pull/21),
each blob's CEK is wrapped by a transit `aes256-gcm96` key created with
`derived=true` (the wrap is bound to the blob's space and digest) and
`exportable=false`, held in an OpenBao local to the region. `ingot-openbao`
is that server. It runs as a real `bao server` (`openbao/config.hcl`:
TCP listener, raft storage) rather than dev mode, so the KEK persists.

`ingot-openbao-init` runs on every `make up`:

1. First boot: `bao operator init` with one unseal share. The share and the
   root token are written to the `ingot-openbao-init` volume (`/init`).
   Dev-only custody; nothing here is a production secret.
2. Every boot: unseal if sealed; enable `transit` if missing; create
   `transit/keys/region-kek` (`aes256-gcm96`, `derived=true`,
   `exportable=false`) if missing; write the `ingot-region-kek` policy
   (encrypt, decrypt, rewrap on that key and nothing else); recreate ingot's
   token with the fixed id above. The token carries OpenBao's default TTL
   (768h) and nothing renews it, so a stack left up for more than 32 days
   needs `make down && make up` to mint a fresh one.

An `ingot-openbao` container restart comes back sealed until the init
service re-runs; `make up` does that. `make clean` removes both volumes
together, which keeps the unseal share and the storage it opens in step.

Follow-on (not yet wired): replace the stored unseal share with
`seal "transit"` against a central OpenBao, so the appliance authenticates
to central at boot, unwraps its barrier key, and unseals.

## Keys and Proofs

- `../../generated/keys/ingot.pem` - Ingot agent key (Ed25519), the key
  behind `did:web:ingot`
- `../../generated/proofs/hilt-ingot-s3-proof.txt` - hilt → ingot delegations
  for `/s3/request/authorize` and `/s3/bucket/{create,delete,info,list}`
  (issuer `did:web:hilt`, audience `did:web:ingot`, subject `did:web:hilt`)

`did:web:ingot` is also what the `hilt-init` registrar registers as the
**us-west-1 provider** — hilt only accepts S3 RPC invocations issued by the
tenant's registered provider, and each tenant records its provider DID at
provisioning. A hilt-postgres volume or snapshot that predates the did:web
identity still holds ingot's did:key as the provider: `hilt-init` reports it
as already registered (hilt treats a region held by another DID the same
way) and hilt then rejects every ingot invocation. Start such a stack fresh
(`make clean`, then regenerate proofs with `--force` so the stale did:key
audience is replaced).

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
- plc (service_healthy)
- ingot-postgres (service_healthy)
- ingot-openbao-init (service_completed_successfully; itself waits on
  ingot-openbao service_healthy)

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
# Server state
docker compose exec ingot-openbao bao status          # Initialized true, Sealed false

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
