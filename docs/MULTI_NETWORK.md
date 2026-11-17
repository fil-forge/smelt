# Multiple storage networks

Smelt runs one storage network by default: one hilt holding tenants and one
ingot serving S3 for a region. The `hilt` and `ingot` sections of `smelt.yml`
declare more of each, so a stack can carry two networks and three regions the
way the Fil One console sees them.

A **network** is one hilt and the gateways registered against it. A tenant on
a network is region-free: its access keys work at every region the network
serves, and its buckets may live in any of them. A key cannot reach a second
network, because the two hold separate identities and separate authorization.

Run the two-network topology with:

```bash
SMELT_MANIFEST=manifests/networks-2.yml make up
```

## The manifest

```yaml
version: 1
hilt:
  nodes:
    - name: hilt
    - name: hilt-1
ingot:
  nodes:
    - name: ingot
      region: us-east-9
    - name: ingot-1
      region: eu-central-9
    - name: ingot-2
      region: us-west-9
      hilt: hilt-1
```

| Field | Applies to | Meaning |
|---|---|---|
| `name` | hilt, ingot | The compose service name, which is also the DNS name on `forge-network` and the `did:web:` label. Lowercase, starting with a letter. Defaults to `hilt`/`ingot` for the first and `hilt-1`, `ingot-1`, … after it. |
| `image` | hilt, ingot | Overrides `HILT_IMAGE`/`INGOT_IMAGE` for this instance alone. |
| `region` | ingot | The S3 region this gateway serves. One gateway per region. A single-gateway stack may leave it out, which keeps `INGOT_REGION` from the shell. |
| `hilt` | ingot | The hilt holding this region's tenants. Defaults to the first. |

`count: N` is shorthand for N entries with everything defaulted, the same as
the `piri` section. Both sections may be left out, which is the single
network smelt has always run.

The generator refuses a manifest that cannot work: two gateways claiming one
region, a gateway naming a hilt that is not declared, a hilt with no gateway
(its network would serve nothing), or more than one gateway with no region.

It also refuses names that would collide once generation derives the rest. An
instance brings a registrar or a vault with it, so a hilt called `hilt-init`
is not a new service but the first hilt's registrar; and the proof file names
a hilt and a gateway joined by a dash, so the pairs (`a-b`, `c`) and (`a`,
`b-c`) would both write `a-b-c`, handing one gateway a delegation meant for
the other.

## What each instance gets

Every hilt and every gateway is a separate principal with separate state.
Nothing is shared except the piri nodes and the services around them.

| Per hilt | Per gateway |
|---|---|
| Its own Ed25519 identity, `generated/keys/<name>.pem`, published as `did:web:<name>` | Its own identity, `generated/keys/<name>.pem`, published as `did:web:<name>` |
| A database on the shared `hilt-postgres` | A database on the shared `ingot-postgres` |
| Its own OpenBao, `<name>-vault`, holding its tenant and access-key private keys | Its own OpenBao, `<name>-openbao`, holding the region KEK |
| An `upload → hilt` customer-add proof, and its own sprue product id | A `hilt → ingot` proof for the `/s3/*` commands |
| A registrar, `<name>-init`, that registers its gateways as the providers for their regions | A rendered config at `generated/ingot/<name>.yaml` |

The region KEK is per gateway because the key belongs to the region, which is
what the regional security design calls for. The tenant keys are per hilt
because a tenant belongs to a network.

## Ports

The leading hilt and gateway keep the ports they have always had, so a
single-network stack is unchanged. The rest take the next free pair, each
service next to its vault.

| Service | Port | Vault | Port |
|---|---|---|---|
| `hilt` | 15110 | `hilt-vault` | 15112 |
| `hilt-1` | 15113 | `hilt-1-vault` | 15114 |
| `hilt-2` | 15115 | `hilt-2-vault` | 15116 |
| `hilt-3` | 15117 | `hilt-3-vault` | 15118 |
| `ingot` | 15130 | `ingot-openbao` | 15132 |
| `ingot-1` | 15133 | `ingot-1-openbao` | 15134 |
| `ingot-2` | 15135 | `ingot-2-openbao` | 15136 |
| `ingot-3` | 15137 | `ingot-3-openbao` | 15138 |

`hilt-postgres` stays on 15111 and `ingot-postgres` on 15131, each serving
every instance of its kind. Four of each fit the reserved ranges, and the
generator refuses more: a fifth would put its vault on 15120 or 15140, which
plc and swarf hold.

## Generated files

`smelt generate` (which `make up` runs) writes:

- `generated/compose/hilt.yml` and `generated/compose/ingot.yml`, which the
  root `compose.yml` includes. `systems/hilt` and `systems/ingot` hold no
  compose file of their own, the way `systems/piri` has not for some time.
- `generated/ingot/<name>.yaml`, one rendered gateway config per gateway,
  from `systems/ingot/config/config.yaml.tmpl`.
- `generated/openbao/<vault>.hcl`, one rendered server config per vault, from
  the template in that service's `openbao/` directory. Raft refuses two nodes
  under one id, so each names itself.
- `generated/keys/networks.tsv`, a `<hilt> <ingot> <region>` line per
  gateway. The proof script and each hilt's registrar read it, so the shell
  steps and the compose files always describe the same topology.

## Using the stack

`scripts/s3-key.sh` talks to `hilt` and `ingot`, the leading pair. To mint a
key on another network, point it at that hilt and gateway:

```bash
SMELT_HILT=hilt-1 SMELT_INGOT=ingot-2 PROFILE=smelt-net2 make s3-key
```

A key minted on one network is rejected by the other: the tenant it belongs
to exists only in its own hilt, and the gateway for a region accepts
authorization only from the hilt it is registered against.

## Fil One against two networks

`SMELT=true pnpm deploy:local` in `fil-one/fil-one` points the console's
Forge dev regions at this stack. Its `S3Region` entries and their network ids
have to match the regions declared here; see that repo's README, "Running
against smelt".
