# PLC (Mock did:plc Directory)

A minimal in-memory stand-in for the public [PLC directory](https://plc.directory).
Hilt publishes tenant `did:plc` identities (genesis operations, key rotations,
tombstones) to this service instead of the real directory, keeping local test
data off the public internet.

The service is dependency-free Go (`main.go`) built by Docker Compose from this
directory — there is no published image.

## Services

- **plc** - Mock did:plc directory (HTTP)

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15120 | 80 | plc | PLC directory API |

## Endpoints

URL shapes match `ucantone`'s `did/plc` `DirectoryClient`/`Resolver`:

- `POST /{did}` - Publish a `plc_operation` or `plc_tombstone` (dag-json body).
  Operations are stored raw, per-DID, append-only.
- `GET /{did}/log/last` - The last published operation, byte-for-byte.
- `GET /{did}` - A DID document derived from the last operation's
  `verificationMethods` (404 once tombstoned).
- `GET /health` - Liveness probe.

## Non-goals

Deliberately not validated (this is a dev mock, not a PLC implementation):

- Operation signatures and rotation-key authority
- `prev` CID chain integrity
- did:plc identifier derivation from the genesis operation

## Configuration

None. State is in-memory and lost on restart.

## Used By

- hilt (`HILT_PLC_DIRECTORY=http://plc:80`)
