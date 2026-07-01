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
empty `indexer.endpoint` and `mailer: nop`; the delegator still validates indexing/egress
delegations at startup, so those proofs are generated even though neither service runs.)

## Topology

- **Box:** `root@23.83.66.244` (Servers.com Calibnet, hostname `ff`), Ubuntu, key-only SSH.
- **Bundle `core`** — `sprue` (upload) + `signing-service` + `delegator` + `postgres` +
  `minio` + `dynamodb-local`. Project `forge-staging-core`.
- **Bundle `piri`** — one `piri-0` storage node (sqlite + filesystem). Project `forge-staging-piri`.
- The bundles talk over **public `https://*.staging.fil.one` URLs**, fronted by the host's
  existing Caddy (`caddy-guppy.service`). The host Lotus Eth RPC (`0.0.0.0:1234`) is reached
  from containers via `host.docker.internal:host-gateway`.
- **No** indexer / IPNI / redis / Anvil / smtp4dev in staging.

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
  sprue.pem signing-service.pem delegator.pem payer-key.hex
  piri-0.pem piri-0-wallet.hex
/mnt/data/fil-one/forge/                  # persistent data on the ZFS pool
  postgres/ minio/ dynamodb/ piri-0/
```

## Prerequisites

- **DNS:** A records for `sprue` / `signing-service` / `delegator` / `piri-0` under
  `staging.fil.one` → `23.83.66.244`, `proxied = false`. Apply
  [`environments/staging/dns/fil-one-staging.tf`](../environments/staging/dns/fil-one-staging.tf)
  via `fil-one/infrastructure`.
- **Calibnet Forge contract addresses** — already filled in
  [`environments/staging/smart-contracts.env`](../environments/staging/smart-contracts.env),
  the **single source of truth** for the chain id, RPC URL, and every contract address.
  Configs reference these as `${VAR}` and provision renders them in — no duplication,
  nothing to fill by hand. Wallet addresses live in
  [`environments/staging/wallets.env`](../environments/staging/wallets.env), written by keygen.
- **Dev machine:** `op` (1Password CLI, signed in), `ssh` to the box, `go` (for keygen),
  `ucantool` (`go install github.com/fil-forge/ucantool@latest`), and foundry's `cast`
  (for `staging-fund-payer`; install via <https://getfoundry.sh>).

## 1. One-time: generate keys, wallets, proofs

Run once, ever (re-running mints fresh keys and overwrites the 1Password item):

```bash
make staging-keygen          # = go run ./cmd/smelt staging keygen
```

This generates the Ed25519 identities, three random EVM wallets (payer / delegator
transactor / piri owner), Postgres + MinIO secrets, stores all private material in the
single 1Password item `op://Fil One/FilOne Forge Staging`, writes the UCAN proofs to
`environments/staging/proofs/`, and **writes the three public wallet addresses into
`environments/staging/wallets.env`** (`PAYER_ADDRESS` from there renders into piri's
config). It prints the three EVM addresses.

## 2. Fund the wallets and commit

Two token types are needed:

- **tFIL (gas)** — fund all three addresses (in `wallets.env`) from the
  [Calibnet faucet](https://faucet.calibnet.chainsafe-fil.io).
- **USDFC (storage payments)** — fund `PAYER_ADDRESS` from the
  [Calibnet USDFC faucet](https://forest-explorer.chainsafe.dev/faucet/calibnet_usdfc).
  This faucet caps out at **10 USDFC/day**. The USDFC must then be *deposited into the
  FilecoinPay contract* before piri can create a proof set — that's a separate on-chain
  step, [`make staging-fund-payer`](#5b-fund-the-payers-filecoinpay-account), run around
  deploy time (below).

Then commit the generated artifacts:

```bash
git add environments/staging/proofs environments/staging/wallets.env
git commit -m "staging: add delegation proofs and wallet addresses"
```

Contract addresses are already set in `smart-contracts.env`, so there is nothing else to
fill in. Keep `wallets.env` handy — top up both balances periodically. (Mailer is `nop` —
there is no email login in staging; see Known risks.)

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
make staging-provision-core            # sprue-config.yaml, delegator.yaml, secrets.env + key files
make staging-provision-piri            # piri-0 key files
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
FilecoinPay account, then deploy `piri`:

```bash
make staging-deploy-core               # sprue + signing-service + delegator + deps
make staging-allowlist-piri            # add piri's DID to the delegator allow list
make staging-fund-payer                # deposit USDFC into FilecoinPay (see 5b below)
make staging-deploy-piri               # piri-0
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

### 5b. Fund the payer's FilecoinPay account

`piri init` step `[4/6]` ("Setting up proof set") asks FilecoinPay to lock up a fixed
amount (~0.9 USDFC) on the payer's behalf. **Lockup can only draw on funds deposited *into*
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
can't run, so register explicitly once both bundles are healthy:

```bash
ssh root@23.83.66.244
cd /root/fil-one/forge/environments/staging/core
PIRI_DID=$(docker compose -p forge-staging-piri exec piri-0 /usr/bin/piri identity parse /keys/piri.pem | grep -oE 'did:key:z[a-zA-Z0-9]+')
docker compose -p forge-staging-core exec sprue \
  sprue client admin provider register "$PIRI_DID" https://piri-0.staging.fil.one /proofs/piri-0-proof.txt
docker compose -p forge-staging-core exec sprue \
  sprue client admin provider weight set "$PIRI_DID" 100 100
```

(The committed `piri-0-proof.txt` must be mounted into the sprue container, or pass it by
path — adjust if needed.)

## 7. Verify

```bash
# Health
ssh root@23.83.66.244 'cd /root/fil-one/forge/environments/staging/core && docker compose -p forge-staging-core ps'
ssh root@23.83.66.244 'cd /root/fil-one/forge/environments/staging/piri && docker compose -p forge-staging-piri ps'

# did:web resolution through Caddy
# Only sprue and delegator serve a did.json. signing-service does NOT — it has no
# /.well-known/did.json route by design (it resolves its own DID from an in-memory
# document, and no peer ever resolves did:web:signing-service: piri uses the
# configured DID only as the signing-invocation audience, and the signed response
# is an EIP-712 signature verified on-chain, not a did:web-resolved UCAN receipt).
# So a 404 here is expected, not a failure.
for h in sprue delegator; do curl -fsS "https://$h.staging.fil.one/.well-known/did.json" >/dev/null && echo "$h ok"; done

# End-to-end (note: no mailer in staging — email-based login is unavailable; see Known risks):
#   guppy login ... ; SPACE=$(guppy space generate) ; randdir --size 10KB --output /tmp/d
#   guppy upload source add $SPACE /tmp/d ; guppy upload $SPACE ; guppy retrieve $SPACE <CID> /tmp/out
```

## 8. Rollback

Re-deploy a previous pinned commit (deploy checks it out on the box for you):

```bash
FORGE_REF=<previous-commit> make staging-deploy-core
FORGE_REF=<previous-commit> make staging-deploy-piri
```

Image versions are pinned per bundle in `versions.env`, so rolling back the application
versions is deterministic. Data/schema rollback is out of scope.

## Known risks / verify-as-you-go

These are deliberate first-step assumptions to confirm during the manual deploy:

1. **RPC scheme (http vs ws).** Configs use `http://host.docker.internal:1234/rpc/v1` per the
   design doc; the local dev stack used `ws://`. If signing-service/delegator/piri fail to
   connect or watch the chain, switch to `ws://host.docker.internal:1234/rpc/v1` (Lotus serves
   both on `:1234/rpc/v1`) by editing `LOTUS_RPC_URL` in `environments/staging/smart-contracts.env`
   — the single place it's defined — then re-provision and re-deploy.
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
   them. The piri base config keeps non-resolving `[ucan.services.indexer]`/`[publisher]`
   sections so `piri init` accepts the config; if piri rejects or misbehaves, remove them.
4. **Auto-created tables/buckets.** The delegator is expected to create its DynamoDB tables;
   `minio-init` creates sprue's buckets; sprue runs its own Postgres migrations. If the
   delegator does not auto-create tables, create `delegator-allow-list` and
   `delegator-provider-info` manually.
5. **No mailer.** staging runs `mailer.type: "nop"`, so no validation emails are sent and
   guppy's email-based login step won't complete. End-to-end upload verification therefore
   needs an alternative login path (e.g. a pre-provisioned space/account).
6. **Image pinning.** `versions.env` ships rolling `:main` placeholders — pin every application
   image to a `@sha256:` digest before a real deploy (`docker buildx imagetools inspect ...`).
