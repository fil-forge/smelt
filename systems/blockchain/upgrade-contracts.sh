#!/usr/bin/env bash
# Upgrades the PDPVerifier and FilecoinWarmStorageService (FWSS) proxies in
# the committed baseline (state/anvil-state.json) to the versions built from
# fil-forge/filecoin-services at FILECOIN_SERVICES_REF, the same way the
# Calibration and mainnet proxies were upgraded: deploy a new implementation,
# announce it, then upgradeToAndCall with migrate(). Proxy addresses do not
# change; FWSS gets a new StateView and libraries, whose addresses are written
# to state/deployed-addresses.json and the piri configs.
#
#   systems/blockchain/upgrade-contracts.sh
#
# The script boots a throwaway chain from the baseline, upgrades it, stops it
# so the entrypoint dumps the state, and replaces state/anvil-state.json with
# that dump. Run `make clean && make up` afterwards to boot the new baseline.
#
# Needs docker, git and jq. Contracts are built and deployed in the Foundry
# image, so the host Foundry version does not matter.
set -euo pipefail

FILECOIN_SERVICES_REF="${FILECOIN_SERVICES_REF:-08d9e619ca3c6cf5e764112445869e6c4f5492dd}"
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

  send() { cast send --unlocked --from "$DEPLOYER" "$@" >/dev/null; }
  create() {
    local root="$1"; shift
    forge create --root "$root" --unlocked --from "$DEPLOYER" --broadcast "$@" |
      awk '/Deployed to:/ { print $3 }'
  }
  next_initializer() { echo $(( $(cast to-base "$(cast storage "$1" "$initializable_slot")" 10) + 1 )); }

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

  local pdp_impl finality
  finality=$(cast call "$PDP_VERIFIER" 'getChallengeFinality()(uint256)' | awk '{ print $1 }')
  pdp_impl=$(create /src/service_contracts/lib/pdp src/PDPVerifier.sol:PDPVerifier \
    --constructor-args "$(next_initializer "$PDP_VERIFIER")" "$finality")
  upgrade_proxy "$PDP_VERIFIER" "$pdp_impl" "$(cast calldata 'migrate()')"

  local sc=/src/service_contracts sig_lib rails fwss_impl view
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

  echo "PDPVerifier $(cast call "$PDP_VERIFIER" 'VERSION()(string)') impl=$pdp_impl" >&2
  echo "FWSS $(cast call "$FWSS" 'VERSION()(string)') impl=$fwss_impl view=$view" >&2
  printf '{"SignatureVerificationLib":"%s","Rails":"%s","FilecoinWarmStorageServiceStateView":"%s"}\n' \
    "$sig_lib" "$rails" "$view"
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

echo "Building fil-forge/filecoin-services at $FILECOIN_SERVICES_REF..."
git clone -q https://github.com/fil-forge/filecoin-services.git "$work/src"
git -C "$work/src" -c advice.detachedHead=false checkout -q "$FILECOIN_SERVICES_REF"
git -C "$work/src" submodule update -q --init --recursive --depth 1 --jobs 8
# PDPVerifier builds with its own foundry.toml (optimizer_runs = 1); built
# with the service contracts' settings it exceeds the contract size limit.
for root in service_contracts/lib/pdp service_contracts; do
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$work/src:/src" -w "/src/$root" \
    --entrypoint forge "$FOUNDRY_IMAGE" build --skip test --skip script >/dev/null
done

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
  --entrypoint bash "$FOUNDRY_IMAGE" /scripts/upgrade-contracts.sh --inside)

echo "Dumping state..."
docker stop -t 120 "$chain" >/dev/null
[ -s "$work/output/anvil-state.json" ] || { echo "the chain did not dump its state" >&2; exit 1; }
cp "$work/output/anvil-state.json" "$here/state/anvil-state.json"

old_view=$(contract FilecoinWarmStorageServiceStateView)
new_view=$(jq -r .FilecoinWarmStorageServiceStateView <<<"$new_addresses")
jq --argjson new "$new_addresses" '.contracts += $new' "$addresses" >"$work/addresses.json"
mv "$work/addresses.json" "$addresses"
for config in "${piri_configs[@]}"; do
  sed -i.bak "s/$old_view/$new_view/" "$config" && rm -f "$config.bak"
done

echo "Done. Updated state/anvil-state.json, state/deployed-addresses.json and the piri configs."
