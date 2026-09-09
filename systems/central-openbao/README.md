# Central OpenBao (appliance seal root)

The central OpenBao of the
[regional security and key management RFC](https://github.com/fil-one/RFC/pull/21).
Each appliance runs its own OpenBao ([ingot-openbao](../ingot/) here) whose
storage is sealed by a transit key held on this instance. At boot the
appliance presents its seal token, central unwraps the appliance's barrier
key, and the appliance unseals. Revoking that token here is the RFC's
startup-kill lever: the appliance never starts again, and everything on its
disk stays unreadable.

This instance is deliberately separate from [hilt-vault](../hilt/). It is
the one Fil One endpoint that operator-controlled hardware talks to, so it
holds nothing else: no tenant keys, no Hilt state.

## Services

- **central-openbao** — `openbao/openbao:2.6` in server mode, raft storage
  on `central-openbao-data`.
- **central-openbao-init** — one-shot: initializes and unseals
  `central-openbao`, then provisions the `seal/` transit engine, the
  appliance seal key, policy, and token (`init.sh`).

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15210 | 8200 | central-openbao | OpenBao HTTP API |

## What init does

On every `make up`:

1. First boot: `bao operator init` with one unseal share. The share and the
   root token are written to the `central-openbao-init` volume (`/init`).
   Dev-only custody; production roots this instance in an HSM or KMS.
2. Every boot: unseal if sealed; enable transit at `seal/` if missing;
   create `seal/keys/ingot-openbao` (`aes256-gcm96`, non-derived,
   `exportable=false`) if missing; write the `appliance-ingot-openbao`
   policy (`update` on `seal/encrypt/ingot-openbao` and
   `seal/decrypt/ingot-openbao`, nothing else); recreate the appliance's
   seal token: orphan, periodic (24h, renewed by the appliance's seal), fixed
   id `dev-ingot-openbao-seal-token` (override with
   `INGOT_OPENBAO_SEAL_TOKEN=... make up`; `systems/ingot/compose.yml` reads
   the same variable).

`make clean` removes both volumes together, which keeps the unseal share and
the storage it opens in step.

## The kill lever

```bash
# Root token for admin actions (dev custody on the init volume; not printed here)
ROOT=$(docker run --rm -v smelt_central-openbao-init:/init alpine cat /init/root-token)

# Revoke the appliance: it fails to unseal on its next start
docker compose exec -e BAO_TOKEN="$ROOT" central-openbao bao token revoke dev-ingot-openbao-seal-token
docker compose restart ingot-openbao        # stays sealed / exits with a seal error

# Reinstate: re-run the provisioner, then start the appliance again
docker compose up central-openbao-init
docker compose up -d ingot-openbao
```

## Production notes

Per the RFC, the production shape adds: TLS on the listener, an appliance
token bound to the region's egress CIDR (and single-use where the deployment
permits), an HSM or KMS root for this instance, and an operator for central
(an open question in the RFC). None of those change the appliance side.

## Volumes

- `central-openbao-data` - raft storage (appliance seal keys)
- `central-openbao-init` - unseal share + root token (dev-only custody)

## Used By

- ingot-openbao (`seal "transit"` in `systems/ingot/openbao/config.hcl`)
