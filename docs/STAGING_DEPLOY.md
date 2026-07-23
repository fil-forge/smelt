# Staging Deployment Runbook

How to manually deploy the Forge stack to the **staging** box.

> **Scope (first step):** manual deploy, no CI. Two independently-deployed Compose
> bundles on one VM, secrets injected from 1Password at provision time.

## Architecture & rationale

Forge services are each released independently to GHCR. Staging composes them into
**version-pinned** Compose manifests held in this repo — the unit of deployment. The
design rests on four ideas:

1. **One version-pinned artifact per bundle.** Each bundle's `versions.env` pins exact
   image references (digests in production use). A deploy applies one set; rollback is
   re-deploying a previous pinned commit. No rolling `:latest`/`:main`.
   _Note: in the initial version, where we deploy manually (no GitHub Actions automation),
   we keep rolling `:main` reference._
2. **Two bundles, deployed independently.** `core` (sprue + signing-service + delegator
   - their dependency containers) and `piri` (the storage node) deploy and roll back on
     their own cadence. They communicate over **public `https://*.staging.fil.one` URLs**
     (fronted by the host's Caddy), not single-network Docker DNS — so the split is real.
3. **Dependencies are per-environment.** Postgres + S3 (MinIO) + DynamoDB run as
   containers in the stack; persistent data lives on the box's ZFS pool
   (`/mnt/data/fil-one/forge`). The chain RPC is the **host Lotus node** (Calibnet,
   `0.0.0.0:1234`), reached via `host.docker.internal:host-gateway` — no Anvil.
4. **Config in Git, secrets in 1Password.** Non-secret config (compose, `config.env`,
   committed proofs) lives in the repo. Secret values live in the single 1Password item
   `op://Fil One/FilOne Forge Staging` and are rendered onto the box at provision time —
   never committed, never written to a developer's local disk, never in CI.

There is **no** indexer / IPNI / redis / Anvil / mailer in staging. (sprue runs with an
empty `indexer.endpoint` and `mailer: nop`; piri's base config omits the
`[ucan.services.indexer]`/`[publisher]` sections, which disables claim caching and IPNI
announcements; the delegator still validates indexing/egress delegations at startup, so
those proofs are generated even though neither service runs. ingot doesn't need an
indexer either — its reads resolve from its local `blob_locations` registry, Known
risk #11.)

### Rationale

The goal is to get the staging deployment up as quickly as possible. We are optimising for learning
and discovering unknown unknowns. See [Open Questions in the Walking Skeleton Notion page](https://app.notion.com/p/filecoin/FilOne-SP-Side-Appliance-Walking-Skeleton-3847631f2825806f876ac0fd478e5a68?source=copy_link#3847631f2825806d90bcc385affd9659).

The two-bundle split is deliberate to force decoupling between Forge core services (operated by
FilOne in production) and storage nodes (operated by storage providers in production).

### Next Steps

Major Gaps:

1. **Upstream ingot fix for the object write path** — PutObject currently fails (Known
   risk #13); until an ingot release closes it, the §8 smoke test stops at bucket
   creation.
2. Write an automated script for the Hilt/Ingot end-to-end workflow ([§8](#8-end-to-end-uploaddownload-test)).
3. Add a smoke test to the staging deploy that runs the end-to-end workflow.
4. Persistent Vault for hilt (the dev-mode Vault is in-memory — see Known risk #8).
5. Fix the malformed did:web documents (libforge): every service's did.json renders the
   verification-method fragment as `#%23key-0` (a double `#` — libforge
   `identity/identity.go` passes an already-prefixed `"#key-0"` to `doc.Fragment`) and
   `controller: null`. Harmless to the current stack's validators, but strict DID
   tooling would reject it; one-line fix in libforge, all services inherit it on their
   next image.

Improvements:

1. Pin the images to digests in `versions.env` (currently rolling `:main`).
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
to a Kubernetes deployment.

## Topology

- **Box:** `root@23.83.66.244` (Servers.com Calibnet, hostname `ff`), Ubuntu, key-only SSH. Learn
  more in [Servers.com Calibnet Box Runbook](https://www.notion.so/filecoin/Servers-com-Calibnet-Box-Runbook-36b7631f2825802b8e3ac9f25eadcc34#3907631f282580e198e2dfbc1e1a47ad)
- **Bundle `core`** — `sprue` (upload) + `signing-service` + `delegator` + `hilt` (tenant
  management) + `plc` (did:plc directory, **internal-only** — no public route) + `hilt-vault`
  + `postgres` + `minio` + `dynamodb-local`. Project `forge-staging-core`.
- **Bundle `piri`** — one `piri-0` storage node (postgres + filesystem) + `ingot` (S3
  gateway) + their shared `postgres`. Project `forge-staging-piri`.
- **Postgres layout** — one shared instance per bundle: a dedicated `admin` superuser plus
  one role + database per service (core: `sprue`, `hilt`, `plc`; piri: `piri_0`, `ingot`),
  created by each bundle's `postgres-init` one-shot.
- The bundles talk over **public `https://*.staging.fil.one` URLs**, fronted by the host's
  existing Caddy (`caddy-guppy.service`). The host Lotus Eth RPC (`0.0.0.0:1234`) is reached
  from containers via `host.docker.internal:host-gateway`.
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
  sprue-config.yaml delegator.yaml secrets.env piri-base-config.toml
  ingot-config.yaml piri-secrets.env
  sprue.pem signing-service.pem delegator.pem hilt.pem payer-key.hex
  piri-0.pem piri-0-wallet.hex ingot.pem
/mnt/data/fil-one/forge/                  # persistent data on the ZFS pool
  postgres/                               # core bundle's shared Postgres (sprue, hilt, plc)
  minio/
  dynamodb/
  piri-0/
  piri-postgres/                          # piri bundle's shared Postgres (piri_0, ingot)
  ingot/                                  # ingot LSM segments, blob spool, token store
```

## Prerequisites

- **DNS:** A records for `sprue` / `signing-service` / `delegator` / `piri-0` under
  `staging.fil.one` → `23.83.66.244`, `proxied = false`. Already set up via https://github.com/fil-one/infrastructure/pull/35.
  **`hilt` and `ingot` need the same records** (manual step in fil-one/infrastructure,
  mirroring PR #35). Deliberately **no `plc` record** — the did:plc directory is internal
  to the core bundle (Known risk #9). No wildcard `*.ingot` record either — the S3
  endpoint is path-style only (Known risk #10).
- **Calibnet Forge contract addresses** — already filled in
  [`environments/staging/smart-contracts.env`](../environments/staging/smart-contracts.env),
  the **single source of truth** for the chain id, RPC URL, and every contract address.
  Configs reference these as `${VAR}` and provision renders them in — no duplication,
  nothing to fill by hand. Wallet addresses live in
  [`environments/staging/wallets.env`](../environments/staging/wallets.env), written by keygen.
- **Dev machine:** `op` (1Password CLI, signed in), `ssh` to the box, `go` (for keygen),
  `ucantool` (`go install github.com/fil-forge/ucantool@latest`), and foundry's `cast`
  (for `staging-fund-payer`; install via <https://getfoundry.sh>).

## 1. Ensure keys, wallets, proofs exist (idempotent)

```bash
make staging-keygen          # = go run ./cmd/smelt staging keygen
```

The ceremony has **ensure semantics** and is safe to re-run: every field the 1Password
item already holds is reused byte-for-byte (funded wallets, registered DIDs, and shipped
keys survive), and only missing fields are generated and added. Re-run it after pulling a
version that adds new services (e.g. hilt/ingot) — it mints only the new material and
reports which fields were reused vs generated. To rotate a specific secret, delete its
field from the 1Password item (and any proof files signed with it) and re-run.

It covers the Ed25519 identities (incl. `hilt` and `ingot`), three random EVM wallets
(payer / delegator transactor / piri owner), connection secrets (Postgres admin +
per-service passwords, MinIO keys, hilt partner key, hilt vault token, ingot root S3
credentials), stores all private material in the single 1Password item
`op://Fil One/FilOne Forge Staging`, writes the UCAN proofs to
`environments/staging/proofs/` (only missing ones, or those invalidated by a freshly
generated key), and **writes the three public wallet addresses into
`environments/staging/wallets.env`** (`PAYER_ADDRESS` from there renders into piri's
config). It prints the three EVM addresses.

## 2. Fund the wallets and commit

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
fill in. Keep `wallets.env` handy — top up both balances periodically. (Mailer is `nop`,
but it logs the login validation link to sprue's logs rather than dropping it, so guppy
email login still works — see [§8](#8-end-to-end-uploaddownload-test-guppy).)

## 3. Bootstrap the box (first time only)

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

## 4. Provision secrets onto the box (dev machine)

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

This is safe because staging holds no precious data.

After wiping it renders configs and ships key files (via `op read`) into
`/root/fil-one/forge/secrets/`, atomically, no plaintext on your local disk.

Because the wipe is unconditional, re-running provision always discards existing
staging state. Run it on first deploy, after a `make staging-keygen` rotation, or
whenever you want a clean environment — not for an in-place config tweak you don't
want to lose data over.

## 5. Deploy

Deploy `core` first, allow-list the piri DID with the delegator, fund the payer's
FilecoinPay account, deploy `piri`, then run the two cross-bundle registrations:

```bash
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

### 5b. Fund the payer's FilecoinPay account

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

## 6. Register the piri provider with the core (cross-bundle step)

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

### 6b. Register ingot with hilt (cross-bundle step)

In local dev, hilt's `post_start.sh` registers ingot as the regional S3 provider; across
bundles that can't run either. Without this step hilt rejects tenant creation for the
region and every `/s3/*` invocation from ingot.

```bash
make staging-register-ingot
```

The script (`scripts/staging-register-ingot.sh`) derives ingot's did:key via `ingot
whoami` in the piri bundle, then runs `hilt client admin provider add <did> us-west-1`
inside the hilt container (region overridable via `INGOT_REGION` — it must match the
`region` in `ingot-config.yaml` and the `AWS_REGION` S3 clients sign with). Run it after
both bundles are healthy and **before creating any tenants**. Idempotent.

This is the **only** cross-bundle registration hilt needs: its authority to call sprue's
`/customer/add` comes entirely from the committed `hilt-customer-add-proof.txt`, and sprue
resolves `did:web:hilt.staging.fil.one` over https at invocation time.

## 7. Verify

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

## 8. End-to-end upload/download test

The target architecture is deployed: FilOne calls hilt's Tenant API to register tenants
and issue S3 access keys; the data plane uses standard S3 primitives against ingot. The
control plane (tenants, access keys, buckets) is verified end-to-end; the object write
path is currently blocked by an upstream ingot gap (Known risk #13).

### Hilt/Ingot S3 flow

Run after both bundles are healthy and both registrations are done ([§6](#6-register-the-piri-provider-with-the-core-cross-bundle-step),
[§6b](#6b-register-ingot-with-hilt-cross-bundle-step)). Everything runs from your dev
machine against the public URLs.

**1. Create a tenant and an access key** (Tenant API, bearer = the partner key):

```bash
PARTNER_KEY=$(op read "op://Fil One/FilOne Forge Staging/hilt-partner-key")

# Create a tenant in the region ingot is registered under (us-west-1).
curl -si -X PUT "https://hilt.staging.fil.one/tenants/smoke-1" \
  -H "Authorization: Bearer $PARTNER_KEY" -H "Content-Type: application/json" \
  -d '{"region":"us-west-1"}'
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

**2. S3 against ingot.** Two client-side settings matter:

- **Use region `us-west-1`** (set both `AWS_REGION` and `AWS_DEFAULT_REGION`). It is
  the only region that works unconditionally; other regions fail in two distinct ways:
  - `aws s3 mb` / `create-bucket` send the client region as the bucket
    `LocationConstraint`, which ingot checks against its configured region →
    `InvalidLocationConstraint`. Exception: `us-east-1` (S3's legacy default) is
    *omitted* from the request, so it slips past this check.
  - The SigV4 credential-scope region must map to the tenant's registered provider
    when a request reaches hilt's authorizer (`validateRegion` in hilt
    `pkg/rpc/service/auth` → `ErrRegionNotServed`). Ingot authorizes locally when its
    caches already hold the access key's verification key + delegation chains, so a
    wrong-region request can *appear* to work on a warm cache — and then 403 after an
    ingot restart or with a fresh access key. Don't rely on it.
- **Path-style addressing** (no wildcard `*.ingot` DNS/TLS exists). The aws CLI
  has no env var for this — it's a config-file setting; `aws configure set`
  writes it once into `~/.aws/config`.

```bash
export AWS_ACCESS_KEY_ID=<from step 1> AWS_SECRET_ACCESS_KEY=<from step 1>
export AWS_REGION=us-west-1 AWS_DEFAULT_REGION=us-west-1
export AWS_ENDPOINT_URL=https://ingot.staging.fil.one
aws configure set default.s3.addressing_style path   # REQUIRED: path-style only

aws s3 mb s3://smoke-bucket
head -c 10240 </dev/urandom > /tmp/hello.bin
aws s3 cp /tmp/hello.bin s3://smoke-bucket/hello.bin
aws s3 cp s3://smoke-bucket/hello.bin /tmp/out.bin
cmp /tmp/hello.bin /tmp/out.bin && echo "roundtrip ok"
```

> **Current status (verified 2026-07-23):** everything up to and including
> `aws s3 mb` works. **`aws s3 cp` (PutObject) currently fails with
> `InternalError`** — an upstream ingot gap, not a deployment problem; see
> Known risk #13.

Behind the scenes: ingot authorizes each request against hilt (`/s3/request/authorize`,
authority = the committed `hilt-ingot-s3-proof.txt`), spools the object, and ships CAR
segments to sprue → piri over public https. Reads resolve from ingot's local
`blob_locations` registry — no indexer involved (Known risk #11).

### Guppy-based flow (legacy regression check)

guppy is a client — run it **on your dev machine**, pointed at the public staging URLs.
`scripts/staging-guppy.sh` wraps `docker run ghcr.io/fil-forge/guppy:main` with the right
override flags and a persistent local state dir under `generated/staging-guppy/`, so the
one agent identity + delegations survive across the multi-step flow. Nothing runs on the staging box
except reading one log line for login.

Do this after both bundles are healthy, the piri provider is registered ([§6](#6-register-the-piri-provider-with-the-core-cross-bundle-step)),
and the payment bypass is on (Known risk #7). Two staging specifics make it work: login reads
the validation link from sprue's logs (mailer is `nop`, Known risk #5), and provisioning skips
the payment-plan check.

**1. Log in.** Pick an email — it becomes your account identity. This blocks until approved:

```bash
./scripts/staging-guppy.sh login you@fil.org
```

The script prints how to fetch the link. In another terminal, read it from sprue's logs and
open it (it's a public `https://sprue.staging.fil.one/...` URL — browser or `curl`):

```bash
ssh root@23.83.66.244
docker logs "$(docker ps -qf name=sprue)" 2>&1 | grep -i "Validation email" | tail -1
# → ...  "Validation email"  to=you@fil.org  url=https://sprue.staging.fil.one/...
```

Once approved, the blocked `login` returns and stores the account→agent delegation locally.

**2. Generate and provision a space.** `provision` binds the space to the account you logged
in as (same email); it succeeds without a payment plan thanks to the bypass:

```bash
./scripts/staging-guppy.sh space generate        # prints the space DID: did:key:z6Mk...
./scripts/staging-guppy.sh space provision did:key:z6Mk... you@fil.org
```

**3. Upload.** Files must be visible inside the container — the script mounts
`generated/staging-guppy/work/` at `/work`. Drop a test file there (min 1 KB), add it as a
source, then upload:

```bash
head -c 10240 </dev/urandom > generated/staging-guppy/work/hello.bin
./scripts/staging-guppy.sh upload source add did:key:z6Mk... /work/hello.bin
./scripts/staging-guppy.sh upload did:key:z6Mk...
# → Upload completed successfully: bafy...   (the root CID)
```

The upload runs guppy → sprue → piri (`space/blob/add` → `blob/allocate` → HTTP PUT →
`ucan/conclude` → `blob/accept`); PDP proofs follow asynchronously via signing-service.
Confirm the blob landed:

```bash
ssh root@23.83.66.244 'docker logs "$(docker ps -qf name=piri-0)" 2>&1 | tail -30'
```

**4. Download — verify-as-you-go (no indexer).** guppy's normal `retrieve` queries an indexer
to locate blobs, and staging runs none (Known risk #3), so:

```bash
./scripts/staging-guppy.sh retrieve did:key:z6Mk... <root-cid> /work/out
```

may fail to resolve a location — an expected gap on this stack, not a storage failure. To
confirm the bytes really landed, fetch a stored blob straight from piri: retrieval there is an
unauthenticated `GET /piece/<cid>`, where `<cid>` is a stored blob/shard CID (from the upload
output or `./scripts/staging-guppy.sh ls did:key:z6Mk...`; the exact root-CID→piece-CID mapping
is itself a verify-as-you-go detail):

```bash
curl -fsS "https://piri-0.staging.fil.one/piece/<cid>" -o /tmp/piece.bin && ls -l /tmp/piece.bin
```

Making `guppy retrieve` work end-to-end needs an indexer (or, in the future Hilt/Ingot model,
Ingot's own read tier). Reset local guppy state any time with `rm -rf generated/staging-guppy`.

## 9. Rollback

Re-deploy a previous pinned commit (deploy checks it out on the box for you):

```bash
FORGE_REF=<previous-commit> make staging-deploy-core
FORGE_REF=<previous-commit> make staging-deploy-piri
```

Image versions are pinned per bundle in `versions.env`, so rolling back the application
versions is deterministic. Data/schema rollback is out of scope.

## Known risks / verify-as-you-go

These are deliberate first-step assumptions to confirm during the manual deploy:

1. **RPC scheme (http vs ws).** `LOTUS_RPC_URL` in `environments/staging/smart-contracts.env`
   — the single place it's defined — is set to `ws://host.docker.internal:1234/rpc/v1`. It must
   be `ws://`: piri watches for tx confirmations via ChainNotify, a streaming subscription that
   only delivers events over a WebSocket; over plain HTTP the scheduler never fires and receipts
   sit pending forever. We use `ws://` (not `wss://`) because the localhost node is plaintext.
   If Lotus is later fronted by TLS, switch to `wss://` there and re-provision/re-deploy.
2. **Container → host Caddy on `:443`** (handled). Cross-bundle calls and upload→piri callbacks
   use `https://*.staging.fil.one`, which resolves to the box's own public IP. Reaching that IP
   from a container is local delivery, but the traffic arrives over the Docker bridge while the
   stock UFW `:443` rule is scoped to the public NIC — so UFW's default deny-incoming dropped it
   (an i/o timeout; this is what hung `piri init`'s registrar call to the delegator). `staging-bootstrap`
   now adds `ufw allow from 172.16.0.0/12 to any port 443 proto tcp`, mirroring the existing Lotus
   `:1234` rule that already lets containers reach the host. If a container still can't reach a
   `*.staging.fil.one` URL, check `ufw status verbose` for that rule first.
3. **`no-indexer` config.** sprue runs fine with an empty `indexer.endpoint`. The delegator
   _requires_ indexing + egress service DIDs and proofs at startup even though neither service
   runs — that's why keygen still issues those proofs and the delegator config still references
   them. Piri's indexer integration is **disabled** by omitting the
   `[ucan.services.indexer]`/`[publisher]` sections from the base config (requires piri with
   optional-indexer support). Pointing those sections at a non-resolving URL does NOT no-op:
   piri's `blob/accept` POSTs a `claim/cache` invocation to the indexer synchronously, and
   every upload fails when that call can't connect. Note that `piri init` bakes the base
   config into the node's merged config, so a node initialized with the old config keeps
   calling the indexer until the piri bundle is re-provisioned and re-deployed.
4. **Auto-created tables/buckets.** The delegator is expected to create its DynamoDB tables;
   `minio-init` creates sprue's buckets; sprue runs its own Postgres migrations. If the
   delegator does not auto-create tables, create `delegator-allow-list` and
   `delegator-provider-info` manually.
5. **No mailer — but login still works via logs.** staging runs `mailer.type: "nop"`, so no
   validation emails are sent. The `nop` mailer does not silently drop the mail, though — it
   _logs_ the validation link (`SendValidation` logs `to=`/`url=` at Info level). So guppy's
   email login completes by reading the link from sprue's logs and opening it; no smtp4dev or
   alternative account path is needed. See [§8](#8-end-to-end-uploaddownload-test-guppy).
6. **Image pinning.** `versions.env` ships rolling `:main` placeholders — pin every application
   image to a `@sha256:` digest before a real deploy (`docker buildx imagetools inspect ...`).
   This now also covers `HILT_IMAGE`, `PLC_IMAGE` (core) and `INGOT_IMAGE` (piri).
7. **Payment-plan bypass.** sprue runs with `deployment.allow_provision_without_payment_plan: true`
   (in `environments/staging/core/config/sprue/config.yaml.tpl`), so spaces can be provisioned
   without a customer payment plan. Storacha payment plans are a left-over sprue inherited; FilOne
   Forge uses a different billing mechanism and does not need them, so we bypass the check here.
8. **Ephemeral Vault (deliberate).** `hilt-vault` runs HashiCorp Vault in **dev mode** —
   auto-unsealed, KV v2 at `secret`, and **entirely in-memory**. Any restart of the vault
   container (deploy recreate, box reboot — `restart: unless-stopped` brings it back empty)
   **loses every tenant/access-key private key**: existing S3 credentials stop working and
   affected tenants must be deleted and re-created via the Tenant API. Hilt's Postgres rows
   survive, but they reference vault entries that no longer exist. This is an accepted
   staging trade-off (staging holds no precious data); production needs a persistent,
   properly-sealed Vault. Hilt supports only `memory` and `hashicorp` vault backends, so
   there is no simple file-backed alternative.
9. **PLC is internal-only (deliberate).** The did:plc directory runs inside the core bundle
   with no published port, no Caddy route, and no DNS record. Its only consumers are hilt
   (`HILT_PLC_DIRECTORY=http://plc:3000`) and sprue (`deployment.plc_directory`), both
   in-bundle. Consequence: tenant `did:plc` identities are **not publicly resolvable** from
   outside the box. Add a Caddy route + DNS record later if external resolution is ever
   needed.
10. **S3 is path-style only (deliberate).** There is no wildcard `*.ingot.staging.fil.one`
    DNS record or certificate, so virtual-hosted bucket addressing
    (`bucket.ingot.staging.fil.one`) does not work. Every S3 client must force path-style
    (`aws configure set default.s3.addressing_style path`, `forcePathStyle: true`, etc.).
11. **No indexer → ingot reads are local.** ingot resolves blob locations from its own
    `blob_locations` Postgres table (the appliance read tier), not from an indexing-service
    — so the missing indexer doesn't affect the S3 flow. Consequence: wiping ingot's data
    dir or database (e.g. `staging-provision-piri`) orphans the read-side location
    knowledge of previously shipped objects.
12. **provision-piri now also wipes ingot.** The piri bundle wipe covers `piri-0`,
    `piri-postgres`, and `ingot` — buckets, objects, and ingot's location registry vanish.
    Hilt tenants (core bundle) survive, but their buckets' data-plane state is gone;
    re-create buckets after a piri re-provision.
13. **UPSTREAM: ingot PutObject fails — write path ships /blob/add with no space proof
    (verified 2026-07-23, `ingot:main`).** `aws s3 cp` returns `InternalError`; ingot's
    log shows `executing blob add: executing invocation: <cid> is not issued by subject
    and has no proofs` — sprue correctly rejects the `/blob/add` invocation because
    ingot attaches no delegation chain from the bucket's space to its agent. This is a
    known gap in ingot itself: `module.go`'s `provideTokenStore` comment says the write
    path's `/blob/add`/`/index/add` chains are unpopulated ("the delegation cache the
    IAM service fills is the likely future source" — those hilt-issued chains also
    terminate at the *access key's* DID, not ingot's agent DID). Nothing in the staging
    deployment can fix this; it needs an ingot release. Re-run the §8 smoke test after
    the next `INGOT_IMAGE` bump.
14. **Deploy health gate vs. recovered crash: stale RestartCount.** The gate fails any
    container with `RestartCount > 0`, even one that recovered and is now healthy (the
    counter persists for the container's lifetime). If `staging-deploy-*` reports
    `crash-looping (restarts=N)` but `docker compose ps` shows the service healthy,
    clear the counter by recreating just that container (`docker compose -p <project>
    <env-files> up -d --force-recreate <service>`) and re-run the deploy for a clean
    gate. Investigate the original crash in `docker logs` first — the gate flagged a
    real crash, just not necessarily a persistent one.
