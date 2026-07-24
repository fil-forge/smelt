# Staging Deployment Runbook

How to manually deploy the Forge stack to the **staging** box.

> **Scope (first step):** manual deploy, no CI. Two independently-deployed Compose
> bundles on one VM, secrets injected from 1Password at provision time.

## Rationale

The goal is to get the staging deployment up as quickly as possible. We are optimising for learning
and discovering unknown unknowns. See [Open Questions in the Walking Skeleton Notion page](https://app.notion.com/p/filecoin/FilOne-SP-Side-Appliance-Walking-Skeleton-3847631f2825806f876ac0fd478e5a68?source=copy_link#3847631f2825806d90bcc385affd9659).

The two-bundle split is deliberate to force decoupling between Forge core services (operated by
FilOne in production) and storage nodes (operated by storage providers in production).

## Architecture

Forge services are each released independently to GHCR. Staging composes them into
Compose manifests held in this repo — the unit of deployment. The target design (once
deployments are automated) is for these manifests to be **version-pinned** to exact image
digests. In this initial version, where we deploy manually, they instead track the rolling
`:main` tag of each service. The design rests on four ideas:

1. **One version-pinned artifact per bundle (future goal).** The aim is for each bundle's
   `versions.env` to pin exact image references (digests in production use), so a deploy
   applies one set and rollback is re-deploying a previous pinned commit — no rolling
   `:latest`/`:main`. _Until deployments are automated, the initial manual version keeps a
   rolling `:main` reference; pinning to digests comes with the CI/CD work (see Next Steps)._
2. **Two bundles, deployed independently.** `core` (sprue + signing-service + delegator
   - hilt + plc + their dependency containers) and `piri` (the storage node + ingot S3
     gateway) deploy and roll back on their own cadence. They communicate over **public
     `https://*.staging.fil.one` URLs** (fronted by the host's Caddy), not single-network
     Docker DNS — so the split is real.
3. **Dependencies are per-environment.** Postgres + S3 (MinIO) + DynamoDB run as
   containers in the stack; persistent data lives on the box's ZFS pool
   (`/mnt/data/fil-one/forge`). The chain RPC is the **host Lotus node** (Calibnet,
   `0.0.0.0:1234`), reached via `host.docker.internal:host-gateway` — no Anvil.
4. **Config in Git, secrets in 1Password.** Non-secret config (compose, `config.env`,
   committed proofs) lives in the repo. Secret values live in the single 1Password item
   `op://Fil One/FilOne Forge Staging` and are rendered onto the box at provision time —
   never committed, never written to a developer's local disk, never in CI.

There is **no** indexer / IPNI / redis / Anvil / mailer in staging. (sprue runs with an
empty `indexer.endpoint`; piri's base config omits the
`[ucan.services.indexer]`/`[publisher]` sections, which disables claim caching and IPNI
announcements; the delegator still validates indexing/egress delegations at startup, so
those proofs are generated even though neither service runs. ingot doesn't need an
indexer either — its reads resolve from its local `blob_locations` registry; see
[Running without an indexer](#running-without-an-indexer).)

## Topology

- **Box:** `root@23.83.66.244` (Servers.com Calibnet, hostname `ff`), Ubuntu, key-only SSH. Learn
  more in [Servers.com Calibnet Box Runbook](https://www.notion.so/filecoin/Servers-com-Calibnet-Box-Runbook-36b7631f2825802b8e3ac9f25eadcc34#3907631f282580e198e2dfbc1e1a47ad)
- **Bundle `core`** — project `forge-staging-core`
  - `sprue` (upload)
  - `signing-service`
  - `delegator`
  - `hilt` (tenant management)
  - `plc` (did:plc directory, **internal-only** — no public route)
  - `hilt-vault` (persistent, sealed — Raft storage)
  - `hilt-vault-unseal` (sidecar that unseals `hilt-vault` on every restart)
  - `postgres`
  - `minio`
  - `dynamodb-local`
- **Bundle `piri`** — project `forge-staging-piri`.
  - one `piri-0` storage node (postgres + filesystem)
  - `ingot` (S3 gateway)
  - `piri-postgres`
- **Postgres layout**
  - one shared instance per bundle
  - a dedicated `admin` superuser plus one role + database per service created by each bundle's `postgres-init` one-shot
    - core: `sprue`, `hilt`, `plc`
    - piri: `piri_0`, `ingot`
- The bundles talk over **public `https://*.staging.fil.one` URLs**, fronted by the host's existing Caddy (`caddy-guppy.service`).
- The host Lotus Eth RPC (`0.0.0.0:1234`) is reached from containers via `host.docker.internal:host-gateway`.
- **No** indexer / IPNI / redis / Anvil / smtp4dev in staging.
- **Logs** - Logs are automatically forwarded to our Grafana Cloud instance.

Host layout:

```
/root/fil-one/forge/                      # this repo, checked out on the box
  environments/staging/smart-contracts.env  # single source: chain id, RPC, contract addresses
  environments/staging/wallets.env        # wallet addresses (public), written by keygen
  environments/staging/{core,piri}/...    # bundle manifests + committed config
  environments/staging/proofs/*.txt       # committed UCAN proofs
  caddy/forge-staging.caddy               # copied from environments/staging/caddy/
/root/fil-one/forge/secrets/              # provisioned (rendered) files (NOT in git)
  delegator.pem
  delegator.yaml
  hilt.pem
  ingot-config.yaml
  ingot.pem
  payer-key.hex
  piri-0-wallet.hex
  piri-0.pem
  piri-base-config.toml
  piri-secrets.env
  secrets.env
  signing-service.pem
  sprue-config.yaml
  sprue.pem
  vault-secrets.env                       # hilt-vault unseal key + root token (shipped by staging-vault-init)
/mnt/data/fil-one/forge/                  # persistent data on the ZFS pool
  postgres/                               # core bundle's shared Postgres (sprue, hilt, plc)
  minio/
  dynamodb/
  hilt-vault/                             # core bundle's Vault Raft store (survives restarts)
  piri-0/
  piri-postgres/                          # piri bundle's shared Postgres (piri_0, ingot)
  ingot/                                  # ingot LSM segments, blob spool, token store
```

## Prerequisites

- **DNS:** A records for `sprue` / `signing-service` / `delegator` / `piri-0` / `hilt` / `ingot` under
  `staging.fil.one` → `23.83.66.244`, `proxied = false`. (Already set up via [infrastructure#35](https://github.com/fil-one/infrastructure/pull/35) and [infrastructure#37](https://github.com/fil-one/infrastructure/pull/37).)

  Deliberately **no `plc` record** — the did:plc directory is internal
  to the core bundle (see [PLC is internal-only](#plc-is-internal-only)).

  No wildcard `*.ingot` record either — the S3 endpoint is path-style only (see
  [S3 is path-style only](#s3-is-path-style-only)).

- **Calibnet Forge contract addresses** — already filled in
  [`environments/staging/smart-contracts.env`](../environments/staging/smart-contracts.env),
  the **single source of truth** for the chain id, RPC URL, and every contract address.
  Configs reference these as `${VAR}` and provision renders them in — no duplication,
  nothing to fill by hand. Wallet addresses live in
  [`environments/staging/wallets.env`](../environments/staging/wallets.env), written by keygen.
- **Dev machine:**
  - `op` (1Password CLI, signed in)
  - `ssh` to the box
  - `go` (for keygen),
  - `ucantool` (`go install github.com/fil-forge/ucantool@latest`)
  - foundry's `cast` (for `staging-fund-payer`; install via <https://getfoundry.sh>).

## Runbook

### 1. Ensure keys, wallets, proofs exist (idempotent)

```bash
make staging-keygen          # = go run ./cmd/smelt staging keygen
```

The ceremony has **ensure semantics** and is safe to re-run: every field the 1Password
item already holds is reused byte-for-byte (funded wallets, registered DIDs, and shipped
keys survive), and only missing fields are generated and added. Re-run it after pulling a
version that adds new services (e.g. hilt/ingot) — it mints only the new material and
reports which fields were reused vs generated. To rotate a specific secret, delete its
field from the 1Password item (and any proof files signed with it) and re-run.

What's covered:

- the Ed25519 identities (incl. `hilt` and `ingot`)
- three random EVM wallets (payer / delegator transactor / piri owner)
- connection secrets (Postgres admin + per-service passwords, MinIO keys, hilt partner key, ingot root S3
  credentials)

The hilt Vault unseal key + root token are **not** covered here — Vault mints them at
runtime, so they are stored by [`make staging-vault-init`](#5a-initialize-the-vault-cross-cutting-step)
instead (see [Persistent sealed Vault](#persistent-sealed-vault)).

Results:

- stores all private material in the single 1Password item `op://Fil One/FilOne Forge Staging`,
- writes the UCAN proofs to `environments/staging/proofs/` (only missing ones, or those invalidated by a freshly generated key),
- writes the three public wallet addresses into `environments/staging/wallets.env` (`PAYER_ADDRESS` from there renders into piri's
  config)
- prints the three EVM addresses

### 2. Fund the wallets and commit

Two token types are needed:

- **tFIL (gas)** — fund all three addresses (in `wallets.env`) from the
  [Calibnet faucet](https://faucet.calibnet.chainsafe-fil.io).
- **USDFC (storage payments)** — fund `PAYER_ADDRESS` from the
  [Calibnet USDFC faucet](https://forest-explorer.chainsafe.dev/faucet/calibnet_usdfc).
  This faucet caps out at **10 USDFC/day**. The USDFC must then be _deposited into the
  FilecoinPay contract_ before piri can create a proof set — that's a separate on-chain
  step, [`make staging-fund-payer`](#5b-fund-the-payers-filecoinpay-account), run around
  deploy time (below).

Then commit the generated artifacts:

```bash
git add environments/staging/proofs environments/staging/wallets.env
git commit -m "staging: add delegation proofs and wallet addresses"
```

Contract addresses are already set in `smart-contracts.env`, so there is nothing else to
fill in. Keep `wallets.env` handy — top up both balances periodically.

### 3. Bootstrap the box (first time only)

Clones/updates the repo on the box, creates the secrets + data directories, wires the
Forge Caddy snippet into the host's main Caddyfile, opens UFW so containers can reach the
host's Caddy on `:443`, and verifies did:web endpoints (warn-only until the services are
deployed). Idempotent.

```bash
make staging-bootstrap
```

(Override `FORGE_HOST`, `FORGE_DIR`, `MAIN_CADDYFILE`, `CADDY_SERVICE`, `REPO_URL`, `FORGE_REF`,
… as env vars; see `scripts/staging-bootstrap.sh`. `FORGE_REF` defaults to `main`; override it
to deploy an unmerged branch, e.g. `FORGE_REF=staging-deployment make staging-bootstrap`.)

### 4. Provision secrets onto the box (dev machine)

Provision the bundle you're about to deploy (we typically deploy one at a time):

```bash
op signin
make staging-provision-core            # sprue-config.yaml, delegator.yaml, secrets.env + key files (incl. hilt.pem)
make staging-provision-piri            # piri-base-config.toml, ingot-config.yaml, piri-secrets.env + key files (incl. ingot.pem)
```

**Provisioning is destructive — it resets the bundle to a clean slate.**

For each bundle it first removes the running containers and deletes that bundle's persistent data
dirs under `${FORGE_DATA_DIR}`, then recreates them empty. We wipe because Postgres bakes its
password into the data dir at first `initdb`, so a stale dir would keep an old password that no
longer matches the freshly provisioned secret. The next `staging-deploy` rebuilds everything from
scratch (sprue re-runs migrations, the delegator re-creates its DynamoDB tables, `minio-init`
re-creates buckets, piri re-syncs).

For the **core** bundle the wipe also clears the `hilt-vault` Raft store, so the Vault must be
re-initialized: run [`make staging-vault-init`](#5a-initialize-the-vault-cross-cutting-step) after
provisioning core (see [Persistent sealed Vault](#persistent-sealed-vault)).

This is safe because staging holds no precious data.

After wiping it renders configs and ships key files (via `op read`) into
`/root/fil-one/forge/secrets/`, atomically, no plaintext on your local disk.

Because the wipe is unconditional, re-running provision always discards existing
staging state. Run it on first deploy, after a `make staging-keygen` rotation, or
whenever you want a clean environment — not for an in-place config tweak you don't
want to lose data over.

### 5. Deploy

Initialize the Vault, deploy `core` first, allow-list the piri DID with the delegator, fund
the payer's FilecoinPay account, deploy `piri`, then run the two cross-bundle registrations:

```bash
make staging-vault-init                # init + unseal hilt-vault, store keys in 1Password (see 5a)
make staging-deploy-core               # sprue + signing-service + delegator + hilt + plc + deps
make staging-allowlist-piri            # add piri's DID to the delegator allow list
make staging-fund-payer                # deposit USDFC into FilecoinPay (see 5b below)
make staging-deploy-piri               # piri-0 + ingot
make staging-register-piri             # register piri as a storage provider (see 6)
make staging-register-ingot            # register ingot as hilt's regional provider (see 6b)
```

`staging-deploy-*` each sync the box's checkout to `FORGE_REF` (`git fetch` +
`reset --hard`), then pull the pinned images, recreate changed containers, and wait for
healthchecks (fails the deploy if any service stays unhealthy past the timeout). The deploy
**aborts** if the box has uncommitted changes to tracked files (untracked files such
as provisioned secrets are ignored). `FORGE_REF` defaults to `main`; override it to
deploy a branch, tag, or commit, e.g. `FORGE_REF=staging-deployment make staging-deploy-core`.

**Why the allow-list step (order matters).** `piri init` step `[4/7]` ("Requesting
approval to join contract from Storacha") calls the delegator's
`/registrar/request-approval`, which **refuses any DID not on its allow list with a 403**.
In local dev, piri's entrypoint adds its own DID to the shared `dynamodb-local` allow list
before init; across the split bundles that route doesn't exist (the piri bundle can't reach
the core bundle's DynamoDB), so the staging entrypoint drops it. `staging-allowlist-piri`
fills the gap from the core side — it derives piri's DID from its provisioned key and runs
the delegator's `store allow-did` against the core DynamoDB. Run it **after** the delegator
is up (`deploy-core`) and **before** `deploy-piri`, so piri's first init is already
allow-listed. It is idempotent. Skipping it makes `deploy-piri` fail with piri crash-looping
on `registration failed with status: 403`.

**`staging-provision-piri` must also have run first**: the allowlist/register scripts load
`$FORGE_SECRETS_DIR/piri-secrets.env` (shipped by provisioning) into their compose
invocations, and compose aborts on a missing env file — another reason §4 provisions both
bundles before this sequence starts.

#### 5a. Initialize the Vault (cross-cutting step)

`hilt-vault` is a real, sealed Vault (see [Persistent sealed Vault](#persistent-sealed-vault)),
so before deploying core it must be initialized once:

```bash
make staging-vault-init
```

The script (`scripts/staging-vault-init.sh`, developer machine only) syncs the box checkout,
boots **only** the `hilt-vault` container (it needs no secrets), and then:

- if Vault is **uninitialized** (fresh box, or after a re-provision wiped `/vault/file`) it
  runs `vault operator init -key-shares=1 -key-threshold=1`, stores the resulting unseal key +
  root token in the 1Password item (`hilt-vault-unseal-key`, `hilt-vault-root-token`), unseals
  Vault, and enables KV v2 at `secret`;
- if Vault is **already initialized** it reuses the stored keys.

Either way it renders `vault-secrets.env` from 1Password and ships it to the box. It is
idempotent, and secret values never touch local disk or a command line.

**Why the order matters.** Unlike keygen's offline secrets, the unseal key and root token are
minted at _runtime_ by Vault, so they cannot exist until Vault runs. `staging-deploy-core`
consumes `vault-secrets.env` (the `hilt-vault-unseal` sidecar reads the unseal key from it and
hilt reads the root token), and its compose invocation **aborts on the missing env file** if
`staging-vault-init` hasn't run. Run it **after** `staging-provision-core` (which wipes
`/vault/file`) and **before** `staging-deploy-core`.

#### 5b. Fund the payer's FilecoinPay account

`piri init` step `[4/6]` ("Setting up proof set") asks FilecoinPay to lock up a fixed
amount (~0.9 USDFC) on the payer's behalf. **Lockup can only draw on funds deposited _into_
the FilecoinPay contract — not the USDFC sitting in the payer wallet.** So a wallet that the
USDFC faucet just topped up still fails with:

```
Error: creating proof set: failed to send transaction:
InsufficientLockupFunds(Payer=0x…, MinimumRequired=900000000000000000, Available=0)
```

The local dev stack never hits this: its Anvil baseline ships with the deposit and operator
approval baked into the chain state. On Calibnet nobody seeded that, and `piri init` does not
deposit for you (its `setupProofSet` calls `CreateProofSet` directly) — so we do it once, out
of band:

```bash
make staging-fund-payer
```

The script (`scripts/staging-fund-payer.sh`, developer machine only) reads the payer key from
1Password and sends three transactions to Calibnet with `cast`:

1. `USDFC.approve(FilecoinPay, amount)` — let FilecoinPay pull the USDFC.
2. `FilecoinPay.deposit(USDFC, payer, amount)` — credit the payer's FilecoinPay account (this
   is what clears `Available=0`).
3. `FilecoinPay.setOperatorApproval(USDFC, FWSS, …)` — let the warm-storage service lock up
   funds when it creates the proof-set rail (missing this trades the funds error for an
   approval revert).

Contract addresses come from `smart-contracts.env`, the payer from `wallets.env`. Amounts are
baked in but overridable via env vars (`USDFC_DEPOSIT_AMOUNT`, `USDFC_LOCKUP_ALLOWANCE`,
`USDFC_RATE_ALLOWANCE`, `USDFC_MAX_LOCKUP_PERIOD`); defaults are kept **well below 5 USDFC** so
one daily faucet grant (10 USDFC/day) covers a top-up with headroom. It reads current balances
first and **skips the deposit if the account already holds enough** (override with
`FORCE_DEPOSIT=1`), so re-running is safe. The RPC defaults to the public glif Calibnet
endpoint (`CALIBNET_RPC_URL` to override — the box's `host.docker.internal` Lotus URL in
`smart-contracts.env` is container-only and not reachable from your dev machine).

Run it **after** `deploy-core` (nothing here depends on it, but the payer wallet must already
hold USDFC) and **before** `deploy-piri`, so piri's first init finds the lockup funds
available. Like the wallet balances, this is a **periodic top-up chore** — re-run it if
proof-set operations later fail with `InsufficientLockupFunds`.

### 6. Register the piri provider with the core (cross-bundle step)

In local dev, sprue's `post_start.sh` auto-registers piri providers; across bundles that
can't run, so register explicitly once both bundles are healthy. Without this step,
uploads fail with `CandidateUnavailable: no storage providers available`.

```bash
make staging-register-piri
```

The script (`scripts/staging-register-piri.sh`) derives the piri DID from the piri
bundle's provisioned key, then runs the same admin calls as the local post_start hook
inside the sprue container: `sprue client admin provider register <did>
https://piri-0.staging.fil.one /proofs/piri-0-proof.txt` followed by `provider weight set
<did> 100 100`. The committed proofs directory is mounted into sprue at `/proofs` by the
core compose file. Idempotent: an already-registered provider is tolerated and the weight
is simply re-applied.

#### 6b. Register ingot with hilt (cross-bundle step)

In local dev, hilt's `post_start.sh` registers ingot as the regional S3 provider; across
bundles that can't run either. Without this step hilt rejects tenant creation for the
region and every `/s3/*` invocation from ingot.

```bash
make staging-register-ingot
```

The script (`scripts/staging-register-ingot.sh`) derives ingot's did:key via `ingot
whoami` in the piri bundle, then runs `hilt client admin provider add <did> dev-ams`
inside the hilt container (region overridable via `INGOT_REGION` — it must match the
`region` in `ingot-config.yaml` and the `AWS_REGION` S3 clients sign with). Run it after
both bundles are healthy and **before creating any tenants**. Idempotent.

This is the **only** cross-bundle registration hilt needs: its authority to call sprue's
`/customer/add` comes entirely from the committed `hilt-customer-add-proof.txt`, and sprue
resolves `did:web:hilt.staging.fil.one` over https at invocation time.

### 7. Verify

```bash
# Health
ssh root@23.83.66.244 'cd /root/fil-one/forge/environments/staging/core && docker compose -p forge-staging-core ps'
ssh root@23.83.66.244 'cd /root/fil-one/forge/environments/staging/piri && docker compose -p forge-staging-piri ps'

# did:web resolution through Caddy
# Only sprue, delegator, and hilt serve a did.json. signing-service does NOT — it has
# no /.well-known/did.json route by design (it resolves its own DID from an in-memory
# document, and no peer ever resolves did:web:signing-service: piri uses the
# configured DID only as the signing-invocation audience, and the signed response
# is an EIP-712 signature verified on-chain, not a did:web-resolved UCAN receipt).
# So a 404 there is expected, not a failure. ingot serves no did.json either — it
# acts under a did:key.
for h in sprue delegator hilt; do curl -fsS "https://$h.staging.fil.one/.well-known/did.json" >/dev/null && echo "$h ok"; done

# ingot (S3 gateway) health
curl -fsS https://ingot.staging.fil.one/health && echo "ingot ok"

# plc is internal-only BY DESIGN: https://plc.staging.fil.one must NOT resolve.

# End-to-end S3 flow: see §8.
```

### 8. End-to-end smoke test (Hilt/Ingot S3 flow)

The target architecture is deployed: FilOne calls hilt's Tenant API to register tenants
and issue S3 access keys; the data plane uses standard S3 primitives against ingot. The
flow below exercises it end-to-end: tenant and access-key creation, bucket operations, and
the S3 object write/read roundtrip.

Run after both bundles are healthy and both registrations are done ([§6](#6-register-the-piri-provider-with-the-core-cross-bundle-step),
[§6b](#6b-register-ingot-with-hilt-cross-bundle-step)). Everything runs from your dev
machine against the public URLs.

**1. Create a tenant and an access key** (Tenant API, bearer = the partner key):

```bash
PARTNER_KEY=$(op read "op://Fil One/FilOne Forge Staging/hilt-partner-key")

# Create a tenant in the region ingot is registered under (dev-ams).
curl -si -X PUT "https://hilt.staging.fil.one/tenants/smoke-1" \
  -H "Authorization: Bearer $PARTNER_KEY" -H "Content-Type: application/json" \
  -d '{"region":"dev-ams"}'
# → 201; hilt publishes the tenant did:plc (internal plc) and registers it as a
#   customer with sprue via /customer/add (watch sprue's logs to confirm).

# Mint an S3 access key for the tenant. `name` and at least one permission are
# required; valid permissions are the AWS-style strings in hilt's
# pkg/s3perm/s3perm.go (s3:GetObject, s3:PutObject, s3:CreateBucket, ...).
# The secret is returned ONCE — capture it.
curl -si -X POST "https://hilt.staging.fil.one/tenants/smoke-1/access-keys" \
  -H "Authorization: Bearer $PARTNER_KEY" -H "Content-Type: application/json" \
  -d '{"name":"smoke","permissions":["s3:CreateBucket","s3:PutObject","s3:GetObject","s3:ListBucket"]}'
```

**2. S3 against ingot.**

Two client-side settings matter:

- **Use region `dev-ams`** (set in `AWS_REGION`)
- **Path-style addressing** (no wildcard `*.ingot` DNS/TLS exists). The aws CLI
  has no env var for this — it's a config-file setting. Run this once:

  ```bash
  aws configure set default.s3.addressing_style path
  ```

Test script:

```bash
export AWS_ACCESS_KEY_ID=#from step 1
export AWS_SECRET_ACCESS_KEY=#from step 1
export AWS_REGION=dev-ams
export AWS_ENDPOINT_URL=https://ingot.staging.fil.one

aws s3 mb s3://smoke-bucket
head -c 10240 </dev/urandom > /tmp/hello.bin
aws s3 cp /tmp/hello.bin s3://smoke-bucket/hello.bin
aws s3 cp s3://smoke-bucket/hello.bin /tmp/out.bin
cmp /tmp/hello.bin /tmp/out.bin && echo "roundtrip ok"
```

Behind the scenes: ingot authorizes each request against hilt (`/s3/request/authorize`,
authority = the committed `hilt-ingot-s3-proof.txt`), spools the object, and ships CAR
segments to sprue → piri over public https. Reads resolve from ingot's local
`blob_locations` registry — no indexer involved (see
[Running without an indexer](#running-without-an-indexer)).

## 9. Updating a bundle after a new image is published

Once a bundle is up, picking up a freshly-published service image (e.g. a new
`hilt`/`plc` in core, or `ingot`/`piri` in piri) is just a **redeploy** of that bundle:

```bash
make staging-deploy-core               # refreshes core images (hilt, plc, sprue, ...)
make staging-deploy-piri               # refreshes piri images (piri-0, ingot)
```

Because `versions.env` still tracks the rolling `:main` tag, `staging-deploy-*` runs
`docker compose pull` (fetching the new image under the same tag) and then recreates only
the containers whose image digest changed — unchanged services keep running. Data survives:
a redeploy is **not** provisioning, so it does not touch secrets or wipe the ZFS-backed
volumes.

Notes:

- **Do not re-run `make staging-provision-*`** to pick up an image — provisioning is
  destructive (§4) and would wipe the bundle's data.
- **No cross-bundle re-registration is needed** for a plain image bump. The allow-list,
  payer funding, and provider registrations (§§5b, 6, 6b) persist across a redeploy; only
  re-run them if provisioning wiped state or a DID/key changed.
- If a service's **config template** changed with the new image (not just the binary),
  re-provision that bundle so the rendered config is regenerated — for the piri base config
  see the re-provision-then-deploy caveat under
  [Running without an indexer](#running-without-an-indexer).
- Once images are pinned to `@sha256:` digests (see [Improvements](#improvements)), updating
  instead means bumping the digest in `versions.env`, committing, and redeploying — the pull
  becomes a no-op and the new digest drives the recreate.

## 10. Rollback

_This will be applicable in the future, after we have pinned versions._

Re-deploy a previous pinned commit (deploy checks it out on the box for you):

```bash
FORGE_REF=<previous-commit> make staging-deploy-core
FORGE_REF=<previous-commit> make staging-deploy-piri
```

Image versions are pinned per bundle in `versions.env`, so rolling back the application
versions is deterministic. Data/schema rollback is out of scope.

## One-shot reset

To wipe **all** staging data and redeploy both bundles from scratch — the full §4
(provision both bundles) + §5 (vault-init, deploy, fund) + §6 (register) sequence in the
correct order — run:

```bash
# Using the current `main` branch
make staging-reset

# Using a specific branch/commit/tag (e.g. a PR branch)
FORGE_REF=staging-deployment make staging-reset
```

`staging-reset` chains the nine `staging-*` targets via recursive `make` and **aborts on
the first failure** (every step is idempotent, so re-running from the top after a transient
failure — e.g. a flaky Calibnet RPC in `staging-fund-payer` — is safe). It is **destructive**:
it discards Hilt tenants + access keys, piri objects, ingot buckets + `blob_locations`,
Postgres, DynamoDB, MinIO, and the `hilt-vault` Raft store.

**What survives:** all Ed25519 service identities and EVM wallet addresses (only re-shipped
from 1Password, never regenerated — so no re-funding or `wallets.env` re-commit) and all
on-chain state (wallet balances, the payer's FilecoinPay deposit). **What rotates:** the
`hilt-vault` unseal key + root token, since the wiped Vault is re-initialized by
`staging-vault-init` (which overwrites those two 1Password fields — see
[Persistent sealed Vault](#persistent-sealed-vault)).

After it finishes, re-create tenants and buckets via the Tenant API / S3 flow ([§8](#8-end-to-end-smoke-test-hiltingot-s3-flow)).

## Topping up low wallet balances

The staging stack draws on three funded wallets, and their balances deplete over time —
gas is spent on every on-chain transaction, and USDFC is consumed as storage payments
settle. When a balance runs dry the stack fails silently: `piri init` and later proof-set
operations break with `InsufficientLockupFunds`, and uploads stall. Until balance
monitoring/alerting exists (Major Gap #4), check and top up periodically — and whenever an
on-chain step fails unexpectedly.

The addresses are in [`environments/staging/wallets.env`](../environments/staging/wallets.env)
(`PAYER_ADDRESS`, delegator transactor, piri owner). There are two balances to watch:

**1. tFIL (gas) — all three wallets.** Every wallet needs tFIL to send transactions.
Top up from the [Calibnet faucet](https://faucet.calibnet.chainsafe-fil.io) (§2). Check a
balance with `cast`:

```bash
cast balance <address> --rpc-url https://api.calibration.node.glif.io/rpc/v1 --ether
```

**2. USDFC / FilecoinPay lockup — payer only.** Storage payments draw on USDFC that has
been _deposited into the FilecoinPay contract_, not the raw balance in the payer wallet.
Two-step top-up:

- Fund `PAYER_ADDRESS` with USDFC from the
  [Calibnet USDFC faucet](https://forest-explorer.chainsafe.dev/faucet/calibnet_usdfc)
  (caps at 10 USDFC/day).
- Deposit it into FilecoinPay with [`make staging-fund-payer`](#5b-fund-the-payers-filecoinpay-account),
  which reads current balances and skips the deposit if the account already holds enough
  (override with `FORCE_DEPOSIT=1`). Re-run it whenever proof-set operations fail with
  `InsufficientLockupFunds`.

See [§2](#2-fund-the-wallets-and-commit) and [§5b](#5b-fund-the-payers-filecoinpay-account)
for the full first-time funding procedure.

## Configuration notes

Deliberate, verified aspects of how the staging stack is configured.

### Running without an indexer

Staging deliberately runs no indexing-service or IPNI (see
[Architecture](#architecture)). The rest of the stack is
configured to operate cleanly without them, and this is confirmed working:

- **sprue** takes an empty `indexer.endpoint`/`did`, which disables its indexer calls.
- **delegator** still validates the indexing + egress-tracking service DIDs and their proofs
  at startup, so keygen issues those proofs and `delegator.yaml` references them even though
  neither service runs.
- **piri** omits the `[ucan.services.indexer]` and `[publisher]` sections from its base
  config, which turns off claim caching and IPNI announcements.
- **ingot** resolves blob locations from its own `blob_locations` Postgres table (the
  appliance read tier) rather than an indexing-service, so the missing indexer doesn't
  affect the S3 read path.

Because `piri init` bakes the base config into a node's merged config, changing these
sections requires re-provisioning and re-deploying the piri bundle before a node picks them
up.

### Self-initializing state (tables, buckets, migrations)

A freshly-provisioned bundle initializes its own persistent state on first deploy — no
manual setup is needed:

- the **delegator** auto-creates its DynamoDB tables (`delegator-allow-list`,
  `delegator-provider-info`),
- **minio-init** creates sprue's buckets,
- **sprue** runs its own Postgres migrations.

### Payment-plan bypass

sprue runs with `deployment.allow_provision_without_payment_plan: true` (in
`environments/staging/core/config/sprue/config.yaml.tpl`), so spaces can be provisioned
without a customer payment plan. Storacha payment plans are a left-over sprue inherited;
FilOne Forge uses a different billing mechanism and does not need them, so we bypass the
check here.

### Persistent sealed Vault

`hilt-vault` runs HashiCorp Vault with the **integrated Raft** storage backend, persisted
on the ZFS pool at `/mnt/data/fil-one/forge/hilt-vault/`. It is a **real, sealed** Vault —
not dev mode — so tenant/access-key private keys **survive restarts** (deploy recreate, box
reboot, `docker restart hilt-vault`).

Two pieces make that work:

- **`hilt-vault-unseal` sidecar** — a long-running poller (`restart: unless-stopped`) that
  unseals `hilt-vault` whenever it is found sealed, i.e. on every (re)start. It reads the
  single unseal key from `vault-secrets.env`. Vault's `/v1/sys/health` stays unhealthy
  while sealed, so hilt's `depends_on hilt-vault: service_healthy` waits until the sidecar
  has unsealed it.
- **[`make staging-vault-init`](#5a-initialize-the-vault-cross-cutting-step)** — the
  one-time (idempotent) ceremony that runs `vault operator init` against a fresh Vault,
  enables KV v2 at `secret`, stores the unseal key + root token in the 1Password item, and
  ships `vault-secrets.env` to the box. Unlike every other secret, these two values are
  minted at _runtime_ by Vault (not offline by keygen), which is why they have their own
  step and their own env file.

The Vault root token doubles as hilt's client token (`HILT_VAULT_TOKEN`); a scoped
policy + limited token is a future hardening step ([Next Steps](#next-steps)). Hilt supports
only `memory` and `hashicorp` vault backends.

**A re-provision (`make staging-provision-core`) wipes `/vault/file`**, so it discards the
initialized Vault (and thus every tenant access key). Re-run `make staging-vault-init` after
any provision — it re-initializes the empty Vault and overwrites the two 1Password fields.
Hilt's Postgres rows survive a Vault wipe but then reference vault entries that no longer
exist, so re-create affected tenants via the Tenant API. This is acceptable because staging
holds no precious data.

### PLC is internal-only

The did:plc directory runs inside the core bundle with no published port, no Caddy route,
and no DNS record. Its only consumers are hilt (`HILT_PLC_DIRECTORY=http://plc:3000`) and
sprue (`deployment.plc_directory`), both in-bundle. Consequence: tenant `did:plc`
identities are **not publicly resolvable** from outside the box. Add a Caddy route + DNS
record later if external resolution is ever needed.

### S3 is path-style only

There is no wildcard `*.ingot.staging.fil.one` DNS record or certificate, so
virtual-hosted bucket addressing (`bucket.ingot.staging.fil.one`) does not work. Every S3
client must force path-style (`aws configure set default.s3.addressing_style path`,
`forcePathStyle: true`, etc.).

### Provisioning wipes bundle data

`make staging-provision-*` deliberately resets a bundle to a clean slate (see
[§4](#4-provision-secrets-onto-the-box-dev-machine)). For the piri bundle the wipe covers
`piri-0`, `piri-postgres`, and `ingot`: buckets, objects, and ingot's `blob_locations`
registry all vanish, orphaning the read-side location knowledge of previously shipped
objects. Hilt tenants (core bundle) survive, but their buckets' data-plane state is gone —
re-create buckets after a piri re-provision.

## Next Steps

### Major Gaps

1. Write an automated script for the Hilt/Ingot end-to-end workflow ([§8](#8-end-to-end-smoke-test-hiltingot-s3-flow)).
2. Add a smoke test to the staging deploy that runs the end-to-end workflow.
3. Give hilt a scoped Vault policy + limited token instead of the root token.
   `hilt-vault` is now persistent and sealed (see
   [Persistent sealed Vault](#persistent-sealed-vault)), but hilt currently
   authenticates with the init-generated **root** token. `staging-vault-init`
   should instead write a KV-scoped policy and mint a limited token for hilt,
   keeping the root token 1Password-only for admin use.
4. Monitoring & alerting for wallet balances. The three wallets' tFIL (gas) and the
   payer's USDFC / FilecoinPay lockup balances are manual top-up chores today (§2,
   [§5b](#5b-fund-the-payers-filecoinpay-account)) with nothing watching them — when
   any runs dry the stack fails silently (piri init / proof-set ops break with
   `InsufficientLockupFunds`, uploads stall). Add a periodic balance check that alerts
   (Grafana Cloud, where logs already ship) before a wallet crosses a low-balance
   threshold, and a runbook entry for topping up — see
   [Topping up low wallet balances](#topping-up-low-wallet-balances).
5. Harden the deploy health gate against recovered-crash false positives. It fails any
   container with `RestartCount > 0` even after the container recovered and is healthy
   (the counter persists for the container's lifetime), so `staging-deploy-*` can report
   `crash-looping (restarts=N)` for a service `docker compose ps` shows healthy. Until it
   is fixed, clear the counter by recreating just that container (`docker compose -p
<project> <env-files> up -d --force-recreate <service>`) and re-run the deploy — after
   checking `docker logs` for the original crash.

### Improvements

1. Pin every application image in `versions.env` to a `@sha256:` digest (currently rolling
   `:main` placeholders) before a real deploy — resolve digests with `docker buildx
imagetools inspect …`; this covers `HILT_IMAGE`, `PLC_IMAGE` (core) and `INGOT_IMAGE`
   (piri) too.
2. Implement GH Action workflows to automatically upgrade pinned versions whenever a new Docker
   image version is pushed by each service repository (sprue, piri, etc).
3. Implement a CI/CD workflow to automatically deploy every commit landed to `main` to the staging box.
4. Add a basic end-to-end test suite (smoke tests).

The outcome of the above: every commit landed to `main` in any Forge repository is automatically
deployed to the staging box (after it passes CI checks in the original repository and the e2e smoke
tests in Smelt).

Before we invest more into improving the Docker Compose setup, we should have a discussion about how
we want to run Forge in production. The current Compose setup has many short-comings, e.g. deploys are
not atomic and there is no support for rolling upgrades or blue/green deployments.

The staging box is already running [K3s](https://k3s.io/), we could consider moving the core bundle
to a Kubernetes deployment
