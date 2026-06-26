# Forge Staging/Production Deployment Architecture

## 1. Background and Proposed Operational Architecture

### 1.1 Where we are today

The Forge stack is a set of services — Sprue (the `upload` service), signing-service, delegator, an indexer, and supporting infrastructure — that today run locally via **Smelt**, a Go tool that generates Docker Compose manifests from a `smelt.yml`. Each service is developed in its own repository and publishes its own container image to **GHCR** (e.g. `ghcr.io/fil-forge/sprue`). Smelt composes these per-service images into one local stack using Compose's `include:` mechanism, with image tags overridable per service via environment variables (defaulting to a rolling `:main` tag). Local dev uses Anvil (a local EVM), containerized Postgres, and MinIO, with cryptographic keys generated at init and committed under `generated/` — appropriate for throwaway local environments, never for production.

This works for development, but it is not a deployment model: the services float on a rolling tag, there is no single artifact that records "what is running," dependencies are local throwaways, and there is no defined process for promoting a known-good set of versions to a shared environment.

### 1.2 What we are proposing

The immediate goal is to **stand up a staging environment as soon as possible**. Production is a future task: it will reuse the same composition and deploy model, but some choices deferred here (notably the placement of Postgres and object storage — see §6) will be settled when production is designed. Everything below targets staging unless stated otherwise.

Staging runs on a **single VM** (initially a bare-metal host already running k3s; the design must apply equally to an EC2-style cloud VM). The proposed operational architecture has four pillars:

1. **Services remain independently released to GHCR.** Each service repository continues to build and publish its own immutable image on release. No change to per-service CI.

2. **A separate deployment repository composes them into one version-pinned manifest.** A dedicated repo (or a dedicated subdirectory of Smelt) holds the _stack manifest_: a single artifact that pins an exact image version for every service simultaneously, plus the topology that wires them together and to their dependencies. This manifest is the unit of deployment. Rolling back means moving from one pinned manifest to another — never updating one service in place.

3. **Dependencies are provided per environment.** In staging, Postgres and S3-compatible object storage are operated as part of the Compose manifest itself (containers in the stack), and the stack uses the **Lotus node already running on the box**, which exposes an Ethereum RPC API locally (replacing Anvil). The production arrangement for Postgres, object storage, and the RPC source is TBD (§6).

4. **Deploys are driven from version control, secrets are not stored in version control.** The pinned manifest and all non-secret configuration live in Git and are the source of truth for what runs. Secret _values_ live in 1Password; the repository declares _which_ secrets are needed and _where_ they come from (as references), and they are rendered onto the box from a developer's machine (§5), never committed or written to a developer's local disk.

### 1.3 Deployment runtime

The near-term runtime is **Docker Compose on the VM**, chosen for fastest time-to-market given the existing Compose-based Smelt manifests. A later migration to **Kubernetes (k3s)** is anticipated once Smelt is extended to generate Kubernetes manifests; the architecture in this spec is chosen so that the version-pinning model, the configuration contract, and the secrets model all carry forward to k3s without redesign (see §9).

---

## 2. Goals and Non-Goals

The goals are primarily operational — how the stack is composed, deployed, and operated. Configuration and secrets handling are treated as subordinate concerns within that operational frame.

### 2.1 Goals

**Composition and versioning**

- **G1 — Single version-pinned artifact.** One manifest pins the exact version (by immutable digest) of every service together. The artifact fully determines what runs on the box.
- **G2 — Atomic deploy and rollback.** Moving the environment to a new set of versions, or back to a previous one, is a single operation over the whole stack. A rollback is "deploy the previous pinned manifest."
- **G3 — Reproducibility.** Re-deploying the same artifact yields the same running stack. There is no reliance on rolling tags or "latest."
- **G4 — Independent service releases preserved.** Composing the stack must not require changing how individual services build or publish. The deployment repo consumes GHCR images; it does not rebuild them.

**Deployment process**

- **G5 — Version-controlled deploys.** The pinned manifest and all non-secret config are in Git; what is deployed is auditable from Git history.
- **G6 — Single-VM target, portable across bare-metal and cloud.** The same process deploys to the existing bare-metal host and to an EC2-style VM with no fundamental change.
- **G7 — Routine deploys can run from GitHub Actions or a developer machine.** Applying a new pinned version set is a scripted operation runnable either way. Configuration and secret provisioning, by contrast, is always a developer-machine activity (G11a).
- **G8 — Right-sized operational burden.** Appropriate for a small team operating 3–4 services on one host. Solutions requiring dedicated platform/ops investment are out of proportion to the workload.

**Configuration and secrets (a subset of the deployment process)**

- **G9 — Infrastructure as code, including the secret topology.** The repo declares what secrets each service needs and where they come from. Config drift invisible to version control is unacceptable.
- **G10 — No plaintext secrets in the repository or in GitHub.**
- **G11 — No plaintext secrets at rest on developer machines.** Rendered config containing secrets is streamed to the host, never written to local disk.
- **G11a — Secret/config provisioning runs only on developer machines.** The 1Password integration is executed exclusively from a developer's machine — as a one-time setup when standing up a new box, or as an infrequent manual step when a config file changes. It is never run from CI, so GitHub never holds 1Password credentials.
- **G12 — Tiered handling of key material.** The fund-bearing wallet key is held more strictly than service identity keys, which are held more strictly than connection strings.

**Forward compatibility**

- **G13 — Clean migration path to k3s.** The versioning model, configuration contract, and secrets model carry forward without redesign.

### 2.2 Non-Goals

- **The mechanism for choosing and pinning versions.** How engineers select image versions and write them into the pinned versions file is out of scope; this document covers only that the artifact is version-pinned and how it is deployed.
- **Database migration and schema-rollback policy.** Out of scope for now; to be addressed separately.
- **Zero-downtime / progressive rollout.** On a single VM we accept a brief recreate window per service. Blue-green and canary are out of scope.
- **Continuous GitOps reconciliation / drift detection.** Deploys are push-and-converge, not continuously reconciled. (k3s migration may revisit this.)
- **Horizontal scaling and multi-node orchestration.** Single VM by design for now.
- **Dynamic / short-lived secrets** (e.g. on-demand database credentials).
- **Automated secret rotation with live propagation.** Rotation is a deliberate, infrequent, developer-run push-then-restart operation.
- **Backup/restore and HA of Postgres and object storage.** Out of scope; staging runs them as Compose containers (§6).
- **Observability/alerting design.** Tracked separately.

---

## 3. The Deployment Artifact (Composition and Version Pinning)

### 3.1 Structure

A **deployment repository** (new repo, or a `deploy/` subtree of Smelt) holds, for the staging environment (and later production):

- A **stack manifest** — a Docker Compose file (or a small set composed via `include:`) that declares every service and its dependency wiring, including the Postgres and object-storage containers (§6).
- A **pinned versions file** — the single place that records the exact image reference for each service. Image references MUST be immutable (a digest such as `ghcr.io/fil-forge/sprue@sha256:…`, or a tag treated as immutable), never a rolling tag such as `:main` or `:latest`. The _mechanism_ by which engineers choose and write these versions is out of scope for this document.
- **Non-secret configuration templates** for each service (see §5 and §7).
- The **deploy scripts** (see §4), ideally implemented as Smelt subcommands so local dev and deploy share one tool.

The pinned versions file is the atomic unit (G1): one commit changes the set of versions, and that commit is the deployable, auditable record of what the environment runs.

### 3.2 Relationship to Smelt

Smelt already composes per-service Compose fragments via `include:` and parameterizes images via environment variables. The deployment artifact follows the same shape so that the local and deployed topologies stay recognizably the same, differing in: pinned digests instead of rolling tags; the box's local Lotus Ethereum RPC instead of Anvil; staging `did:web` identities under the appropriate naming scheme; and environment-appropriate settings (e.g. `deployment.allow_provision_without_payment_plan: false`). Postgres and object storage are still run as containers in staging, as in local dev, but without the throwaway/dev posture. Longer term, when Smelt generates k3s manifests, it should generate the deployment artifact for both runtimes from one source of truth to avoid divergence.

---

## 4. Deployment Process

### 4.1 Overview

Two distinct activities operate at different cadences and from different places:

**A. Configuration / secret provisioning (infrequent, developer-machine only).** Performed when standing up a new box, or when a config file or secret changes. A developer renders each service's config from its committed template, pulling secret values from 1Password, and streams the result onto the VM into a protected directory; standalone key files are shipped the same way. No plaintext touches local disk. This is the only activity that touches 1Password, and it never runs from CI (G11a). Full detail in §5.

**B. Version deploy (routine).** Applies a committed pinned version set to the VM. Steps:

1. **Resolve the artifact.** Check out the deployment repo at the commit being deployed; read the pinned versions file.
2. **Authenticate to GHCR and pull pinned images** on the VM by digest.
3. **Apply the stack** — `docker compose up -d` with the pinned references in place, which recreates only the services whose image or config changed. (Config is whatever was last provisioned in activity A; a version deploy does not re-render secrets.)
4. **Verify health** — wait on each service's healthcheck; treat the deploy as failed if any service does not reach healthy within a timeout.
5. **Record the deploy** — the deployed commit is the record; optionally tag it.

Because activity B does not touch secrets, routine deploys carry no 1Password credentials and can run from CI or from a developer machine (G7). A config change is the one case that requires activity A (developer-run) to precede a restart.

### 4.2 Execution model

- **Configuration / secret provisioning (activity A):** developer-machine only, using the developer's 1Password session and SSH access. Never CI. Infrequent (new box, or a config/secret change).
- **Version deploy (activity B):** runnable from **GitHub Actions** (a workflow checks out the pinned commit, connects to the VM, pulls by digest, applies, verifies health) or from a **developer machine** running the same scripted steps. The workflow holds only what it needs to reach the box and pull from GHCR — never 1Password credentials or raw secret values.

The logic for both lives in scripts (ideally Smelt subcommands), not in workflow YAML, so the execution surfaces share one implementation.

### 4.3 Connecting to the VM

- **Bare-metal:** SSH to a dedicated non-root `deploy` user, with narrowly-scoped `sudo` only for the specific operations that require it (e.g. `chown root` on the wallet key file, restarting the Compose project).
- **EC2-style:** the same SSH path works; alternatively AWS SSM Session Manager can replace standing SSH keys with OIDC-federated, keyless access. The deploy scripts must not assume cloud-specific access so the bare-metal path remains first-class (G6).

### 4.4 Atomicity and rollback

- **Atomic unit:** the pinned versions file. A deploy applies one such set; there is no per-service version drift.
- **Recreate window:** `docker compose up -d` recreates changed containers, so each changed service has a brief unavailability window. Accepted per Non-Goals. Healthchecks plus `depends_on` ordering bound the blast radius.
- **Rollback:** re-deploy the previous pinned commit. Because images are pinned by digest and config was provisioned separately, rollback of the application versions is deterministic. Rollback of **data/schema** is out of scope (database migration policy is deferred — see Non-Goals).
- **Failed deploy:** if health verification fails, the deploy is reported failed; the operator decides whether to roll back to the prior pinned commit. (Automatic rollback on health failure is a possible later enhancement, not required initially.)

---

## 5. Configuration and Secrets Handling

Configuration and secrets are provisioned onto the box as the developer-run activity A (§4.1) — when standing up a new box, or as an infrequent manual step when a config changes. This section specifies the mechanism; it is one concern within the operational process, not a separate system, and it is the only part that touches 1Password.

### 5.1 Approach: 1Password + render-and-ship

Secret values are stored in **1Password**, which the team already uses. The deployment repo holds, per service, a committed **config template** containing `op://` references in place of secret values — this is the declaration of _what secrets exist and where_ (G9). A developer runs a Smelt subcommand that renders each template via the 1Password CLI (`op inject` for templated files, `op read` for standalone key items) and **streams the result directly into an SSH session** that writes it to the VM — no plaintext is written to local disk (G11). The VM stores rendered config and key files in a protected directory (e.g. `/opt/forge/secrets/`), bind-mounted read-only into the containers. This runs only on a developer machine, never in CI (G11a).

### 5.2 Secret inventory and tiers

Secrets are grouped by blast radius (G12):

- **Tier 1 — Fund-bearing key.** `payer-key.hex`, the raw EVM private key the signing-service uses to sign on-chain PDP transactions; effectively a hot wallet. Compromise is catastrophic and irreversible. Never committed, never through GitHub, never on a developer's local disk. Stored in a **separate, tighter-access 1Password vault**; shipped as a `root`-owned `0400` file; encrypted offline backup; rotation is a deliberate, infrequent ceremony.
- **Tier 2 — Service identity keys.** `upload.pem`, `signing-service.pem`, and the delegator identity key (Ed25519 PEMs). Compromise allows service impersonation; rotatable without financial loss. Shipped as standalone files referenced by path; never inlined into config.
- **Tier 3 — Connection/credential strings.** Sprue's Postgres DSN (with password), S3 access/secret keys, and mailer credentials if enabled. Rendered into per-service config from `op://` references. (The Ethereum RPC endpoint is the box's local Lotus node, addressed via host-gateway; see §6. Lotus authorizes its JSON-RPC API with JWT bearer tokens at four permission levels — read, write, sign, admin — but the `eth_*` namespace, read methods, and `MpoolPush` are on the public/read surface. Since the signing-service signs with its own wallet key and uses Lotus only to read chain state and broadcast already-signed transactions, Forge is expected to need at most a **read-level** token, not a sign-level one. If a token is required, it is a Tier 3 secret provisioned the same way; a tokenless/read-only local URL is not.)

### 5.3 Render-and-ship mechanics

- **No local plaintext.** Output is piped directly to the VM; nothing is written locally. Illustrative pattern:

  ```bash
  op inject -i config.prod.yaml.tpl | \
    ssh deploy@host '
      umask 077
      t=$(mktemp /opt/forge/secrets/.cfg.XXXXXX)
      cat > "$t" && mv "$t" /opt/forge/secrets/config.yaml || { rm -f "$t"; exit 1; }
    '
  ```

- **Atomic, correctly-permissioned writes:** write to a temp file, set owner and mode, then rename into place, so a partial transfer never replaces a good file and there is no loose-permission window.
- **Fail loudly:** run with `set -euo pipefail` so a failed `op inject` aborts. Combined with temp-then-rename, a failed render leaves the live config untouched rather than installing a truncated file (critical given §5.6).
- **Per-file invocations:** each file is shipped via its own pipe-to-SSH operation, so one failed render cannot corrupt an unrelated file and the Tier 1 key's stricter ownership stays isolated. Connections may be multiplexed with SSH `ControlMaster`.

### 5.4 Secret declaration (templates)

- Non-secret structure (ports, regions, bucket names, deployment flags, indexer endpoints, staging `did:web` identities) appears as literal values in the committed template.
- Secret values appear only as `op://<vault>/<item>/<field>` references.
- Key files are referenced by their on-host path; the key material is shipped as a separate file, never inlined.
- A redacted, structure-only example of each config (no resolved values) is committed so reviewers can see the shape.

### 5.5 Authentication

Provisioning is always developer-run (G11a): the developer authenticates the 1Password CLI via their own session and writes to the box over their own SSH access. No standing 1Password credential exists anywhere, and none is stored in CI. "Who can provision secrets" equals "who has both a 1Password session and SSH access" — an intentional, controlled set, with the Tier 1 wallet vault restricted to a tighter subset of people than the general config vault.

### 5.6 Service configuration behavior the implementation must respect

Established from service source:

- **Sprue** loads a YAML config via `--config`, with optional `SPRUE_*` env overrides (env > file > defaults). `identity.key_file` (a path) takes precedence over an inline key, so the identity PEM is loaded from a file — never required in config or env. Viper uses `AllowEmptyEnv(true)` and Sprue rejects an empty Postgres DSN at startup; a truncated/empty rendered config fails confusingly at runtime, which is why render-time failure (§5.3) is mandatory.
- **signing-service** is configured primarily via command-line flags (with a `signer.yaml` the flags override); the `SPRUE_*` mechanism does not apply. It loads two key files by path (`--signing-key-path` → Tier 1 wallet; `--service-key-file` → Tier 2 identity) and takes `--rpc-url` and `--service-contract-address` as flags. In staging the `--rpc-url` points at the box's local Lotus node (no token, not a secret). It runs as `user: root` in the dev manifest — the implementation should determine whether staging requires root and drop it if not. The plan should still confirm whether sensitive fields can be read from `signer.yaml`, which matters more if a future environment uses a token-bearing RPC.

---

## 6. Dependencies

The stack depends on three things beyond the application services themselves:

- **Ethereum RPC** — in **staging**, provided by the **Lotus node already running on the box**, which exposes an Ethereum RPC API locally. It is reached over a local URL, carries no token in the basic case, and is therefore not a secret. Lotus is operated independently of this stack and is not part of the deployment artifact; the stack only references its local endpoint. The **production** RPC source is TBD.

  **Addressing (decided):** Compose services reach the host-bound Lotus RPC via a host-gateway alias — `extra_hosts: ["host.docker.internal:host-gateway"]` — and address it as `http://host.docker.internal:1234/rpc/v1`. This was chosen over the docker bridge gateway IP (brittle: the IP varies by network/machine and needs dynamic resolution) and over host networking (`network_mode: host`, which would lose container isolation and Compose service-name discovery and is Linux-only). Host networking's only real advantage — keeping Lotus on loopback — does not apply here, because Lotus binds `0.0.0.0:1234` and is already reachable from the docker bridge; that also means UFW's default-deny on the public interface does not block container→host traffic on the bridge. The bridge gateway IP remains a fallback only if a host-gateway support issue arises. Example:

  ```yaml
  services:
    signing-service:
      extra_hosts:
        - "host.docker.internal:host-gateway"
      # ... rpc-url / config points at http://host.docker.internal:1234/rpc/v1
  ```

  **Security follow-ups (noted, not blocking staging):** (1) Docker manages its own iptables chains that UFW does not control, so UFW deny rules do not reliably filter container traffic — relevant if a container port is ever published. (2) Binding Lotus to `0.0.0.0:1234` is broader than ideal; consider rebinding it to the internal/bridge interface. (3) If Forge requires a Lotus auth token at all (see open question 11), scope it to the minimum needed — expected to be **read-level**, since the signing-service signs with its own wallet key and only reads chain state and broadcasts via Lotus, so neither sign nor admin permission should be handed to the containers. Tracked in §6 rather than as a blocker because staging traffic is host-local.

- **Postgres** — in **staging**, run as a container within the Compose manifest (as in local dev, but without the throwaway/dev posture). Its data persists on a VM volume. The **production** arrangement (managed instance vs. container vs. host service) is TBD.
- **S3-compatible object storage** — in **staging**, likewise run as a container within the Compose manifest. The **production** arrangement is TBD.

In staging, Postgres and object storage are part of the stack the manifest brings up, but their images need not be pinned with the same rigor as the application services (a pinned base image tag is sufficient). Their credentials are Tier 3 secrets provisioned via §5. Backup/restore and durability are out of scope (Non-Goals).

---

## 7. Repository Layout (Proposed)

A starting point for the deployment repo (or Smelt `deploy/` subtree); to be finalized in the plan:

- `environments/staging/` (production added later), containing:
  - the stack manifest (Compose `include:` root), including the Postgres and object-storage containers,
  - the pinned versions file (image references for every service),
  - per-service config templates with `op://` references,
  - redacted structure-only example configs.
- `scripts/` (or Smelt subcommands): `provision-config` (developer-run render-and-ship of config + keys, §5), `deploy` (pull + up + health verify), `rollback`.
- `.github/workflows/`: the version-deploy workflow (thin; calls the `deploy` script; no 1Password access).

What is **never** in the repo: secret values, rendered configs, key material.

---

## 8. Open Questions for the Implementation Plan

1. **Deployment repo vs. Smelt subtree:** new repository or a `deploy/` subdirectory of Smelt? Affects access control and whether the deploy tooling ships as Smelt subcommands.
2. **signing-service config surface:** can `signer.yaml` carry all sensitive fields so flags are dropped, or must some remain flags? (Determines RPC-token exposure in `docker inspect`/`ps`.)
3. **Root in signing-service:** does the staging image require `user: root`? If not, run non-root and set key-file ownership accordingly.
4. **Persistence for the dependency containers:** volume layout and retention for the staging Postgres and object-storage containers (durability/backup remain a Non-Goal, but the volumes must survive `compose up`/restarts).
5. **Health verification and failure policy:** per-service healthcheck definitions, deploy timeout, and whether health failure triggers automatic rollback or just a failed status.
6. **VM access on cloud:** standing SSH `deploy` user, or SSM/keyless for the EC2-style target? Keep bare-metal SSH first-class regardless.
7. **1Password vault layout:** vault names and item/field schema per secret; confirm the access-control split between the general config vault and the Tier 1 wallet vault.
8. **Host secrets directory:** path (`/opt/forge/secrets/` assumed), owning user, the `deploy` user's narrow `sudo` rights, and whether to place it on tmpfs.
9. **Config-change reload:** how a config-only change (provisioned via activity A) triggers the affected service to restart.
10. **Fresh-host provisioning:** the bootstrap runbook so standing up a VM is re-running the documented steps, not an ad-hoc scramble.
11. **Lotus RPC addressing — DECIDED:** containers reach the host Lotus node via `host.docker.internal:host-gateway` at `http://host.docker.internal:1234/rpc/v1` (see §6). Remaining follow-ups: (a) confirm whether Forge calls any Lotus method above the public read/`eth_*`/`MpoolPush` surface — if not, no auth token is needed; if so, a **read-level** Lotus JWT (not sign/admin, since signing is done locally with the service's own wallet key) becomes a Tier 3 secret provisioned via §5; and (b) decide whether to rebind Lotus off `0.0.0.0`.

---

## 9. Migration Path to Kubernetes (k3s)

The architecture is chosen so the three core models survive the runtime change:

- **Version pinning:** the pinned-digest set becomes pinned image digests in k8s manifests (or a versioned Helm chart whose chart version is the atomic pin). The "one artifact pins all services" property is unchanged.
- **Configuration contract:** services continue to read a config file and key files from mounted paths; only the mechanism that populates those paths changes (a mounted Secret/ConfigMap instead of an scp'd file). No application change.
- **Secrets:** the same `op://` templates feed the 1Password Kubernetes Operator (or an `op inject` step that creates k8s Secrets), preserving "declaration in Git, values in the manager." Tier 1 isolation maps onto a separate, tightly-RBAC'd Secret with cluster secrets-encryption-at-rest enabled.
- **Deploy process:** push-and-converge over SSH is replaced by `kubectl apply`/Helm (or, later, a GitOps controller if continuous reconciliation becomes desirable — a deliberate change from the current Non-Goal).

When Smelt is extended to emit k8s manifests, it should generate both the Compose and k8s forms of the deployment artifact from one source to prevent the two representations from diverging.

---

## 10. Alternatives Considered and Rejected

Evaluated against the goals in §2. The first cluster concerns the deployment/composition model; the second concerns secrets handling specifically.

### Deployment and composition

#### 10.1 Rolling tags (`:main`/`:latest`) with no pinned manifest

Deploy by pulling the current rolling tag for each service, as local dev does.

- **Why rejected:** violates G1–G3. Nothing records what is running, "redeploy" is non-reproducible, and there is no coherent rollback target. Rolling tags are correct for local dev and wrong for shared environments.

#### 10.2 Per-service independent deploys (no single atomic artifact)

Deploy each service on its own when it releases, without a combined pinned set.

- **Why rejected:** violates G1–G2. The stack's running state becomes an emergent combination no single artifact captures; rollback and reasoning about compatibility across services become ad hoc. The stated requirement is to pin a _set_ of versions together.

#### 10.3 Kubernetes/k3s now (skip Compose)

Translate to k8s manifests immediately and deploy to the existing k3s.

- **Why rejected (for now):** higher time-to-market cost and concept count than reusing the existing Compose manifests, against G8 and the fast-start intent. It remains the planned next step (§9); the spec is built to migrate cleanly rather than to adopt k8s prematurely.

#### 10.4 GitOps controller (Argo/Flux) on the single box now

Run an in-cluster controller that continuously reconciles the box to Git.

- **Why rejected (for now):** disproportionate operational weight for 3–4 services on one VM (G8) and pulls in a Non-Goal (continuous reconciliation). Reconsidered at k3s migration if it earns its keep.

#### 10.5 Push secrets/config via a host-resident file edited in place

Keep complete configs (with secrets) only on the host, edited directly.

- **Why rejected:** secrets and config topology would live and change out-of-band from version control — no review, no diff, no record of what production runs. Directly violates G9.

### Secrets handling

#### 10.6 Plain env vars rendered into a host `.env` from GitHub secrets

GitHub holds each secret; the deploy renders a `.env` on the host.

- **Why rejected:** GitHub would hold plaintext of every secret, including (naively) the wallet key — violating G10 and our key-handling rules, and concentrating blast radius in CI. The `AllowEmptyEnv(true)` hazard (§5.6) also makes env rendering error-prone.

#### 10.7 SOPS / git-crypt — encrypted secrets committed to the repo

Commit ciphertext; decrypt on the host at deploy.

- **Why rejected:** stores encrypted secret _bytes_ in the repo. Our definition of infra-as-code (G9) is that the repo declares _what exists and where_ — references, not ciphertext. A reasonable lighter-weight option for teams that accept ciphertext-in-repo, but a different philosophy than required.

#### 10.8 Self-hosted HashiCorp Vault (systemd service on the VM)

Vault with Raft storage and a loopback listener; an agent renders config from templates; repo holds policies/roles/templates.

- **Why rejected:** satisfies G9–G11 but the operational burden is disproportionate (G8). A stateful, security-critical service to unseal (the unattended-reboot problem needs cloud KMS or a TPM/HSM on bare metal), back up, and upgrade, with unseal keys whose loss is unrecoverable, plus an AppRole "secret-zero" bootstrap before storing the first secret. The right tool only if we later need dynamic secrets, fine-grained audit, or central rotation across many hosts. Remains the documented graduation path.

#### 10.9 Managed secrets manager other than 1Password (Infisical, Doppler, AWS Secrets Manager)

Same references-in-repo, values-in-manager model with a different provider.

- **Why rejected relative to 1Password:** any could work; 1Password is already in use with team access, its CLI offers first-class reference injection that maps onto render-and-ship, and its vault permissions give Tier 1 isolation for free. A separate manager adds a dependency and provisioning surface for no benefit at this scale. (AWS Secrets Manager becomes more attractive only if the deployment standardizes on EC2 with IAM auth.)

#### 10.10 Application reads the secrets manager directly

Add a secrets-manager client to each service.

- **Why rejected:** invasive per-service changes and couples application code to a backend. Services already load config and keys from files/flags, so render-and-ship meets the goal with no application changes (and preserves G4/G13).
