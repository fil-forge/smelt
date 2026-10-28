# Blockchain System

Local Filecoin blockchain using Anvil for smart contract interactions.

## Services

- **blockchain** - Anvil-based local Filecoin chain with pre-deployed contracts

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15000 | 8545 | blockchain | JSON-RPC endpoint |

## Configuration

No configuration files. Uses environment variables:
- `ANVIL_BLOCK_TIME=3` - Block time in seconds

## Standalone Usage

```bash
cd systems/blockchain
docker compose up -d
```

## Contract versions

The chain boots from the committed baseline in `state/`: `anvil-state.json` holds the deployed contracts and `deployed-addresses.json` their addresses. Each contract that has one reports its version through `VERSION()`:

```bash
cast call --rpc-url http://localhost:15000 0xa513E6E4b8f2a923D98304ec87F64353C4D5C853 'VERSION()(string)'
```

The baseline holds PDPVerifier 3.5.0 and FWSS 1.5.0, deployed by the localdev image that [fil-forge/filecoin-services](https://github.com/fil-forge/filecoin-services) publishes. FWSS runs on the ERC-8167 dispatcher, recorded as `FWSSDispatcher`, and the proxy serves the StateView reads itself, so `FilecoinWarmStorageServiceStateView` is the proxy's address.

To take a newer release, run the image in init mode, wait until it creates `/tmp/ready`, then stop it so it dumps its state, and copy the dump into `state/`:

```bash
docker run -d --name localdev-init -e MODE=init -v "$PWD/out:/output" ghcr.io/fil-forge/filecoin-localdev:main
docker stop -t 120 localdev-init && docker rm localdev-init
cp out/anvil-state.json out/deployed-addresses.json systems/blockchain/state/
make clean && make up
```

A fresh deploy can move contract addresses, so update the configs listed under "Contract Addresses" in `docs/EXTENDING.md`. Saved snapshots carry the old contracts; recapture them after a new baseline.

## Dependencies

None - this is a foundational service.

## Used By

- signing-service
- delegator
- piri
