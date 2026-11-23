# Delegator System

UCAN delegation service (registrar) for storage providers.

## Services

- **delegator** - Issues UCAN delegations to storage providers
- **delegator-postgres** - The delegator's store: its allow list and provider info. The delegator applies its schema migrations on startup.
- **delegator-init** - One-shot: puts every piri-N node from `smelt.yml` on the allow list with the delegator's own `store allow-did` command (`allow-piri.sh`). Every piri-N waits for it to complete.

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15040 | 80 | delegator | Delegator API |
| 15041 | 5432 | delegator-postgres | Delegator store (psql from host, debugging) |

## Configuration

- `config/delegator.yaml` - Delegator service configuration, shared by `delegator` and `delegator-init`

## Keys

- `../../generated/keys/delegator.pem` - Ed25519 identity key
- `../../generated/keys/piri-nodes.did` - The piri-N DIDs `delegator-init` allows, one per line

## Volumes

- `delegator-postgres-data` - The delegator's Postgres data

## Inspecting the allow list

```bash
docker compose exec delegator-postgres psql -U delegator -d delegator
```

To allow another DID by hand:

```bash
docker compose run --rm --entrypoint /usr/bin/registrar delegator-init \
  --config /.delegator.yaml store allow-did did:key:z6Mk...
```

## Standalone Usage

```bash
# Requires blockchain to be running
cd systems/delegator
docker compose up -d
```

## Dependencies

- blockchain (service_healthy)
- delegator-postgres (service_healthy)

## Used By

- piri
