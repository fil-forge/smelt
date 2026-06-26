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
  `ucantool` (`go install github.com/fil-forge/ucantool@latest`).

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

Fund all three addresses (in `wallets.env`) from the
[Calibnet faucet](https://faucet.calibnet.chainsafe-fil.io), then commit the generated
artifacts:

```bash
git add environments/staging/proofs environments/staging/wallets.env
git commit -m "staging: add delegation proofs and wallet addresses"
```

Contract addresses are already set in `smart-contracts.env`, so there is nothing else to
fill in. Keep `wallets.env` handy — top up these balances periodically. (Mailer is `nop` —
there is no email login in staging; see Known risks.)

## 3. Bootstrap the box (first time only)

Clones/updates the repo on the box, creates the secrets + data directories, wires the
Forge Caddy snippet into the host's main Caddyfile, and verifies did:web endpoints
(warn-only until the services are deployed). Idempotent.

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

```bash
make staging-deploy-core               # sprue + signing-service + delegator + deps
make staging-deploy-piri               # piri-0
```

Each syncs the box's checkout to `FORGE_REF` (`git fetch` + `reset --hard`), then
pulls the pinned images, recreates changed containers, and waits for healthchecks
(fails the deploy if any service stays unhealthy past the timeout). The deploy
**aborts** if the box has uncommitted changes to tracked files (untracked files such
as provisioned secrets are ignored). `FORGE_REF` defaults to `main`; override it to
deploy a branch, tag, or commit, e.g. `FORGE_REF=staging-deployment make staging-deploy-core`.

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
for h in sprue signing-service delegator; do curl -fsS "https://$h.staging.fil.one/.well-known/did.json" >/dev/null && echo "$h ok"; done

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
2. **Container → public-IP hairpin.** Cross-bundle calls and upload→piri callbacks use
   `https://*.staging.fil.one`, which resolves to the box's own public IP. If hairpin NAT from
   a container to the host's public IP fails, front the calls via `host.docker.internal` HTTP
   ports instead.
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
