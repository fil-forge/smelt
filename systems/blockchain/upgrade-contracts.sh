#!/usr/bin/env bash
# Upgrades the PDPVerifier and FilecoinWarmStorageService (FWSS) proxies in
# the committed baseline (state/anvil-state.json) to the versions built from
# fil-forge/filecoin-services at FILECOIN_SERVICES_REF, the same way the
# Calibration and mainnet proxies were upgraded. Proxy addresses do not change.
#
# - PDPVerifier, and FWSS up to v1.4.0: deploy a new implementation, announce
#   it, then upgradeToAndCall with migrate(). FWSS gets a new StateView and
#   libraries.
# - FWSS v1.5.0 and later: a v1.4.0 proxy moves to the ERC-8167 dispatcher in
#   one delayed upgrade through FWSSDispatcherTransition, which installs the
#   modules josuke.json lists. StateView stays where it is: it reads FWSS
#   storage through extsload, which the dispatcher still routes.
#
# A proxy already on the version being built is left as it is.
#
#   systems/blockchain/upgrade-contracts.sh
#
# The script boots a throwaway chain from the baseline, upgrades it, stops it
# so the entrypoint dumps the state, and replaces state/anvil-state.json with
# that dump. New contract addresses go to state/deployed-addresses.json and,
# for a new StateView, the piri configs. Run `make clean && make up`
# afterwards to boot the new baseline.
#
# Needs docker, git and jq. Contracts are built and deployed in the Foundry
# image, so the host Foundry version does not matter. FILECOIN_SERVICES_REPO
# may name a local checkout to build a commit that is not pushed yet.
set -euo pipefail

FILECOIN_SERVICES_REPO="${FILECOIN_SERVICES_REPO:-https://github.com/fil-forge/filecoin-services.git}"
FILECOIN_SERVICES_REF="${FILECOIN_SERVICES_REF:-c9b8407125fa74e15abc55f76653bb4948cc0506}"
FOUNDRY_IMAGE="${FOUNDRY_IMAGE:-ghcr.io/foundry-rs/foundry:v1.7.1}"
BLOCKCHAIN_IMAGE="${BLOCKCHAIN_IMAGE:-ghcr.io/fil-forge/filecoin-localdev:main}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
addresses="$here/state/deployed-addresses.json"
# The entrypoint test expects the testdata copy to match the base config.
piri_configs=("$repo/systems/piri/config/piri-base-config.toml" "$repo/testdata/piri-base-config-indexer-on.toml")

# Runs inside the Foundry container, which shares the throwaway chain's
# network namespace, so the chain's RPC is on localhost. The deployer is one
# of anvil's unlocked accounts, so no key is needed.
upgrade_inside() {
  export ETH_RPC_URL=http://localhost:8545
  local initializable_slot=0xf0c57e16840df040f15088dc2f81fe391c3923bec73e23a9662efc9c229c6a00
  local implementation_slot=0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc
  local sc=/src/service_contracts

  send() { cast send --unlocked --from "$DEPLOYER" "$@" >/dev/null; }
  create() {
    local root="$1"; shift
    forge create --root "$root" --unlocked --from "$DEPLOYER" --broadcast "$@" |
      awk '/Deployed to:/ { print $3 }'
  }
  next_initializer() { echo $(( $(cast to-base "$(cast storage "$1" "$initializable_slot")" 10) + 1 )); }
  source_version() { sed -n 's/.*string public constant VERSION = "\(.*\)";.*/\1/p' "$1"; }
  implementation() { cast parse-bytes32-address "$(cast storage "$1" "$implementation_slot")"; }

  # Releases before PDPVerifier 3.4.0 and FWSS 1.3.1 only have the
  # absolute-epoch announcement, and FWSS 1.4.0 removed it, so try the
  # relative one first. Either refuses the upgrade before the announced epoch.
  upgrade_proxy() {
    local proxy="$1" impl="$2" migrate_call="$3" after
    after=$(( $(cast block-number) + 2 ))
    send "$proxy" 'announceUpgradePlan(address,uint96)' "$impl" 2 2>/dev/null ||
      send "$proxy" 'announcePlannedUpgrade((address,uint96))' "($impl,$after)"
    until [ "$(cast block-number)" -ge "$(( after + 1 ))" ]; do sleep 1; done
    send "$proxy" 'upgradeToAndCall(address,bytes)' "$impl" "$migrate_call"
  }

  local pdp_impl finality pdp_version
  pdp_version=$(source_version "$sc/lib/pdp/src/PDPVerifier.sol")
  if [ "$(cast call "$PDP_VERIFIER" 'VERSION()(string)')" = "\"$pdp_version\"" ]; then
    echo "PDPVerifier already runs $pdp_version" >&2
  else
    finality=$(cast call "$PDP_VERIFIER" 'getChallengeFinality()(uint256)' | awk '{ print $1 }')
    pdp_impl=$(create "$sc/lib/pdp" src/PDPVerifier.sol:PDPVerifier \
      --constructor-args "$(next_initializer "$PDP_VERIFIER")" "$finality")
    upgrade_proxy "$PDP_VERIFIER" "$pdp_impl" "$(cast calldata 'migrate()')"
    echo "PDPVerifier $(cast call "$PDP_VERIFIER" 'VERSION()(string)') impl=$pdp_impl" >&2
  fi

  if [ -f "$sc/src/FilecoinWarmStorageService.sol" ]; then
    local sig_lib rails fwss_impl view
    sig_lib=$(create "$sc" src/lib/SignatureVerificationLib.sol:SignatureVerificationLib)
    rails=$(create "$sc" src/lib/Rails.sol:Rails)
    fwss_impl=$(create "$sc" src/FilecoinWarmStorageService.sol:FilecoinWarmStorageService \
      --libraries "src/lib/SignatureVerificationLib.sol:SignatureVerificationLib:$sig_lib" \
      --libraries "src/lib/Rails.sol:Rails:$rails" \
      --constructor-args "$PDP_VERIFIER" "$FILECOIN_PAY" "$USDFC" \
        "$(cast call "$FWSS" 'filBeamBeneficiaryAddress()(address)')" \
        "$SERVICE_PROVIDER_REGISTRY" "$SESSION_KEY_REGISTRY" "$(next_initializer "$FWSS")")
    view=$(create "$sc" src/FilecoinWarmStorageServiceStateView.sol:FilecoinWarmStorageServiceStateView \
      --constructor-args "$FWSS")
    upgrade_proxy "$FWSS" "$fwss_impl" "$(cast calldata 'migrate(address)' "$view")"
    echo "FWSS $(cast call "$FWSS" 'VERSION()(string)') impl=$fwss_impl view=$view" >&2
    printf '{"SignatureVerificationLib":"%s","Rails":"%s","FilecoinWarmStorageServiceStateView":"%s"}\n' \
      "$sig_lib" "$rails" "$view"
    return
  fi

  local pinned_hash
  pinned_hash=$(sed -n 's/^bytes32 constant FWSS_DISPATCHER_CODE_HASH = \(0x[0-9a-fA-F]*\);/\1/p' \
    "$sc/src/FWSSDispatcherTransition.sol")
  if [ "$(cast keccak "$(cast code "$(implementation "$FWSS")")")" = "$pinned_hash" ]; then
    echo "FWSS already runs the ERC-8167 dispatcher; later releases arrive as josuke migrations" >&2
    echo '{}'
    return
  fi

  local deployed dispatcher migration transition
  deployed=$(cd "$sc" && FILBEAM_BENEFICIARY=$(cast call "$FWSS" 'filBeamBeneficiaryAddress()(address)') \
    forge script script/FWSSDispatcherTransitionLocal.s.sol --rpc-url "$ETH_RPC_URL" --broadcast \
      --unlocked --sender "$DEPLOYER" --slow --skip test)
  dispatcher=$(awk '$1 == "dispatcher" { print $2 }' <<<"$deployed")
  migration=$(awk '$1 == "migration" { print $2 }' <<<"$deployed")
  transition=$(awk '$1 == "transition" { print $2 }' <<<"$deployed")
  [ -n "$dispatcher" ] && [ -n "$migration" ] && [ -n "$transition" ] ||
    { echo "$deployed" >&2; echo "the transition deployment did not report its addresses" >&2; exit 1; }
  upgrade_proxy "$FWSS" "$transition" "$(cast calldata 'migrate(address)' "$migration")"
  [ "$(implementation "$FWSS")" = "$dispatcher" ] || { echo "FWSS does not run the dispatcher" >&2; exit 1; }
  echo "FWSS on the ERC-8167 dispatcher $dispatcher, migration $migration," \
    "$(cast call "$FWSS" 'selectors()(bytes4[])' | tr -cd , | wc -c | awk '{ print $1 + 1 }') selectors" >&2
  printf '{"FWSSDispatcher":"%s"}\n' "$dispatcher"
}

if [ "${1:-}" = "--inside" ]; then
  upgrade_inside
  exit
fi

for bin in docker git jq; do
  command -v "$bin" >/dev/null || { echo "$bin not found" >&2; exit 1; }
done

work="$(mktemp -d)"
chain="smelt-contract-upgrade-$$"
cleanup() {
  docker rm -f "$chain" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

echo "Building $FILECOIN_SERVICES_REPO at $FILECOIN_SERVICES_REF..."
git clone -q "$FILECOIN_SERVICES_REPO" "$work/src"
git -C "$work/src" -c advice.detachedHead=false checkout -q "$FILECOIN_SERVICES_REF"
git -C "$work/src" submodule update -q --init --recursive --depth 1 --jobs 8
sc="$work/src/service_contracts"

# FWSS v1.5.0 and later run on the ERC-8167 dispatcher, which only the evm
# assembler builds, so the build and the upgrade use an image with it.
image="$FOUNDRY_IMAGE"
if [ ! -f "$sc/src/FilecoinWarmStorageService.sol" ]; then
  evm_revision=$(sed -n 's/^EVM_REVISION := //p' "$sc/Makefile")
  image="smelt-foundry-evm:$evm_revision"
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "Building the evm assembler at $evm_revision..."
    docker build -q -t "$image" --build-arg FOUNDRY_IMAGE="$FOUNDRY_IMAGE" --build-arg EVM_REVISION="$evm_revision" \
      -f "$here/foundry-evm.Dockerfile" "$here" >/dev/null
  fi
  cp "$here/FWSSDispatcherTransitionLocal.s.sol" "$sc/script/"
fi

# PDPVerifier builds with its own foundry.toml (optimizer_runs = 1); built
# with the service contracts' settings it exceeds the contract size limit.
for root in service_contracts/lib/pdp service_contracts; do
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$work/src:/src" -w "/src/$root" \
    --entrypoint forge "$image" build --skip test --skip script >/dev/null
done
if [ "$image" != "$FOUNDRY_IMAGE" ]; then
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$work/src:/src" -w /src/service_contracts \
    --entrypoint make "$image" erc8167 >/dev/null
fi

echo "Booting the committed baseline..."
mkdir -p "$work/output"
docker run -d --name "$chain" \
  -v "$here/state/anvil-state.json:/app/anvil-state.json:ro" \
  -v "$work/output:/output" \
  "$BLOCKCHAIN_IMAGE" >/dev/null
until docker exec "$chain" test -f /tmp/ready 2>/dev/null; do sleep 1; done

echo "Upgrading proxies..."
contract() { jq -er ".contracts.$1" "$addresses"; }
new_addresses=$(docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  --network "container:$chain" -v "$work/src:/src" -v "$here:/scripts:ro" \
  -e DEPLOYER="$(jq -er .deployer.address "$addresses")" \
  -e PDP_VERIFIER="$(contract PDPVerifier)" \
  -e FWSS="$(contract FilecoinWarmStorageService)" \
  -e FILECOIN_PAY="$(contract FilecoinPayV1)" \
  -e USDFC="$(contract MockUSDFC)" \
  -e SERVICE_PROVIDER_REGISTRY="$(contract ServiceProviderRegistry)" \
  -e SESSION_KEY_REGISTRY="$(contract SessionKeyRegistry)" \
  --entrypoint bash "$image" /scripts/upgrade-contracts.sh --inside)

echo "Dumping state..."
docker stop -t 120 "$chain" >/dev/null
[ -s "$work/output/anvil-state.json" ] || { echo "the chain did not dump its state" >&2; exit 1; }
cp "$work/output/anvil-state.json" "$here/state/anvil-state.json"

old_view=$(contract FilecoinWarmStorageServiceStateView)
new_view=$(jq -r '.FilecoinWarmStorageServiceStateView // empty' <<<"$new_addresses")
jq --argjson new "$new_addresses" '.contracts += $new' "$addresses" >"$work/addresses.json"
mv "$work/addresses.json" "$addresses"
updated="state/anvil-state.json and state/deployed-addresses.json"
if [ -n "$new_view" ]; then
  for config in "${piri_configs[@]}"; do
    sed -i.bak "s/$old_view/$new_view/" "$config" && rm -f "$config.bak"
  done
  updated="$updated, and the StateView address in the piri configs"
fi

echo "Done. Updated $updated."
