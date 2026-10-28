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
  (`openbao/init.sh`, with the shared bootstrap in
  `../common/openbao/bootstrap.sh`).

`INGOT_PG_STANDBY=1` adds a synchronous standby for ingot-postgres; see
[Synchronous standby](#synchronous-standby-opt-in).

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15130 | 80 | ingot | S3 API (path-style only) + `/health` + `/.well-known/did.json` |
| 15131 | 5432 | ingot-postgres | PostgreSQL |
| 15132 | 8200 | ingot-openbao | OpenBao HTTP API |

## Configuration

- `config/config.yaml` - mounted at `/etc/ingot/config.yaml` (ingot's default
  search path). `INGOT_*` env vars in ingot's environment set any of its
  config keys, with `_` for `.`; `compose.yml` passes on only the ones it
  lists. Ingot builds before
  [fil-forge/ingot#228](https://github.com/fil-forge/ingot/pull/228) apply
  them only to keys the file sets or ingot gives a default.
- `INGOT_LOCAL_BLOB_MAX_BYTES` - ingot's local blob budget in bytes
  (`local_blob_max_bytes`), passed through from the shell when set, e.g.
  `INGOT_LOCAL_BLOB_MAX_BYTES=200000000000 make up`. Unset or empty leaves the
  file's 0: no budget. `config.yaml` sets the key so that builds from
  [fil-forge/ingot#218](https://github.com/fil-forge/ingot/pull/218), which
  added the budget, to #228 honour the variable too. Images that predate #218
  ignore it.
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

## Synchronous standby (opt-in)

`INGOT_PG_STANDBY=1` gives ingot-postgres a synchronous streaming standby,
so every commit waits until the standby has flushed it, as with a standby on
a second machine at the same site. `INGOT_PG_STANDBY_RTT` adds a round trip
between the two. The Makefile then chains
[`compose.pg-standby.yml`](../../compose.pg-standby.yml) after `compose.yml`
in every compose call; without the variable the stack is unchanged.

```bash
# Export both for the session: a `make up` or `make redeploy` without them
# recreates ingot-postgres as a plain server, whose commits no longer wait.
export INGOT_PG_STANDBY=1 INGOT_PG_STANDBY_RTT=0.5ms
make up
```

| Variable | Default | Meaning |
|---|---|---|
| `INGOT_PG_STANDBY` | `0` | `1` adds the standby |
| `INGOT_PG_STANDBY_RTT` | `0ms` | round trip added between primary and standby, as a `tc netem` delay: `500us`, `0.5ms`, `2ms` |
| `NETSHOOT_IMAGE` | `nicolaka/netshoot:v0.16` | image of the container that applies the delay |

What the overlay changes:

- **ingot-postgres** becomes the primary: `synchronous_standby_names =
  ingot_standby`, `synchronous_commit = on`, `max_slot_wal_keep_size = 1GB`,
  and an `hba_file` (`postgres-standby/pg_hba.conf`) that also admits
  replication connections. All are command-line settings, nothing is written
  to `ingot-postgres-data`. On an empty volume `postgres-standby/primary.sh`
  initializes the database with the image's plain arguments first: the
  image's temporary init server would otherwise wait forever for a standby
  that cannot reach it.
- **ingot-postgres-standby** is a hot standby on `ingot-postgres-standby-data`
  (`postgres-standby/standby.sh`). On first boot it clones the primary with
  `pg_basebackup`, creating the physical replication slot `ingot_standby`, and
  streams through that slot as `application_name` `ingot_standby`, the name
  the primary waits for. It clones again when its data no longer matches the
  primary: another cluster's data (the primary's volume was recreated or
  restored from a snapshot while this one stayed) or a slot the primary no
  longer has. It connects as the `ingot` superuser: a replication role of its
  own would take a commit, which would wait for the standby. ingot starts once
  the standby streams.
- **ingot-postgres-netem** owns the standby's network namespace and adds
  `INGOT_PG_STANDBY_RTT` to everything the standby sends
  (`postgres-standby/netem.sh`). netem shapes only what an interface sends, so
  the whole round trip goes on the standby's side, where it delays each flush
  acknowledgement. It needs the kernel's netem qdisc (`sch_netem`), which
  Docker Desktop and stock distribution kernels have. On a kernel without it a
  nonzero round trip stops the stack from starting, and this container's log
  says why; `0ms` adds no qdisc and works anywhere.

Check that the standby streams and is synchronous:

```bash
docker compose exec ingot-postgres psql -U ingot -d ingot -c \
  "SELECT application_name, state, sync_state, write_lag, flush_lag, replay_lag FROM pg_stat_replication"
# ingot_standby | streaming | sync | ...
```

Check that a commit takes about one round trip longer: time single-row
inserts in a scratch database, then the same with the session's
`synchronous_commit` set to `local`, which skips the wait.

```bash
docker compose exec ingot-postgres createdb -U ingot standby_probe
docker compose exec ingot-postgres psql -U ingot -d standby_probe -c "CREATE TABLE t (at timestamptz)"
{ echo '\timing on'; for i in $(seq 200); do echo 'INSERT INTO t VALUES (now());'; done; } \
  | docker compose exec -T ingot-postgres psql -U ingot -d standby_probe -qAt \
  | awk '/^Time:/ { print $2 }' | sort -g | awk '{ v[NR] = $1 } END { print "median", v[int(NR / 2)], "ms" }'
# Again with "SET synchronous_commit = local;" after '\timing on'.
```

Things to know:

- If the standby stops, every commit on ingot-postgres waits for it and
  ingot's writes hang, as on a real synchronous pair. Postgres does not fall
  back to asynchronous replication on its own. `make up` starts it again.
- Switching the overlay off keeps the slot on the primary, and a plain server
  has no `max_slot_wal_keep_size`, so its WAL then grows without bound. Run
  `make clean`, or drop the slot:
  `docker compose exec ingot-postgres psql -U ingot -d ingot -c "SELECT pg_drop_replication_slot('ingot_standby')"`.
- A plain `docker compose logs` outside make answers "no such service" for
  the overlay's services; give it the file too:
  `COMPOSE_FILE=compose.yml:compose.pg-standby.yml docker compose logs ingot-postgres-standby`.
- Snapshots do not capture `ingot-postgres-standby-data`; after a restore
  the standby clones the restored primary.

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
- `ingot-postgres-standby-data` - the standby's copy of ingot-postgres, only
  with `INGOT_PG_STANDBY=1`

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
# Create a hilt tenant + access key and save them as AWS CLI profile "smelt"
# (region, endpoint and path-style addressing included). TENANT=... and
# PROFILE=... override the defaults; the secret is never printed.
make s3-key

aws --profile smelt s3api create-bucket --bucket test-bucket
aws --profile smelt s3api put-object --bucket test-bucket --key hello.txt --body ./hello.txt
aws --profile smelt s3api get-object --bucket test-bucket --key hello.txt /tmp/hello.txt
aws --profile smelt s3api list-buckets
```

The profile stores `endpoint_url`, which needs AWS CLI v2.13 or newer. To
drive the tenant API by hand instead, see the smoke test in
[systems/hilt/README.md](../hilt/README.md).

The saved key keeps working across `make down && make up`: hilt-vault keeps
the tenant's signing key on a volume. Rerun `make s3-key` after `make clean`,
or after restoring a snapshot saved before the key was minted. If hilt has
the tenant but not its key (a snapshot saved before the vault persisted), the
script provisions the next free tenant id (`dev-2`, ...) and writes a fresh
key to the profile.

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
