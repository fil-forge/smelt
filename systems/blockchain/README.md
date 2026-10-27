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

The image deploys PDPVerifier 3.1.0 and FWSS 1.1.0. `upgrade-contracts.sh` brings the baseline to the versions [fil-forge/filecoin-services](https://github.com/fil-forge/filecoin-services) deploys, currently PDPVerifier 3.5.0 and FWSS 1.5.0. It upgrades both proxies in place, as on Calibration and mainnet, so their addresses stay the same.

FWSS 1.5.0 runs on the ERC-8167 dispatcher, which routes each function to a module. The script moves the FWSS proxy there from 1.4.0 with `FWSSDispatcherTransition`, as upstream's v1.5.0 rollout does, and records the dispatcher's address as `FWSSDispatcher` in `state/deployed-addresses.json`. The proxy serves the StateView reads itself; the StateView that piri's config names keeps working, because it reads FWSS storage through `extsload`, which the dispatcher still routes. Building the dispatcher needs the `evm` assembler, so the script builds `foundry-evm.Dockerfile` once, at the revision filecoin-services pins.

```bash
systems/blockchain/upgrade-contracts.sh
make clean && make up
```

To move to a newer fork commit, set `FILECOIN_SERVICES_REF` and run the script again; it upgrades whatever the committed baseline holds and leaves a proxy that already runs the built version alone. Moving FWSS from one dispatcher release to a later one takes that release's josuke migration, which the script does not run; it stops with an error before touching the baseline. To build a commit that is not pushed, point `FILECOIN_SERVICES_REPO` at a local checkout. Saved snapshots carry the old contracts; recapture them after an upgrade.

## Dependencies

None - this is a foundational service.

## Used By

- signing-service
- delegator
- piri
