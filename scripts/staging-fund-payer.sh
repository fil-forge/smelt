#!/usr/bin/env bash
# Fund the staging payer's FilecoinPay account so `piri init` can create a proof
# set — DEVELOPER-MACHINE ONLY.
#
# `piri init` step [4/6] ("Setting up proof set") calls FilecoinPay to lock up a
# fixed amount (~0.9 USDFC) on the payer's behalf. Lockup can only draw on funds
# *deposited into* the FilecoinPay contract — NOT the USDFC sitting in the payer
# wallet. A freshly funded wallet therefore still fails with
# `InsufficientLockupFunds(..., Available=0)` until we move USDFC into FilecoinPay
# and let the warm-storage service lock it up. The local dev stack never needs
# this: its Anvil baseline ships with the deposit + operator approval baked in.
#
# This runs three transactions against Calibnet with the payer key, read from
# 1Password (never written to disk, never logged):
#   1. USDFC.approve(FilecoinPay, amount)                — let FilecoinPay pull USDFC
#   2. FilecoinPay.deposit(USDFC, payer, amount)         — credit the payer's account
#   3. FilecoinPay.setOperatorApproval(USDFC, FWSS, ...) — let warm-storage lock it up
#
# cast passes function signatures inline, so there are no ABI files to keep in
# sync — only the three stable signatures below.
#
# Amounts are baked in but overridable via env vars. Defaults are kept well below
# 5 USDFC because the Calibnet USDFC faucet
# (https://forest-explorer.chainsafe.dev/faucet/calibnet_usdfc) only sends 10
# USDFC/day — one faucet grant must cover a top-up with headroom to spare.
#
# Prerequisites on the dev machine:
#   - an authenticated 1Password session (`op signin`)
#   - foundry's `cast` on PATH (https://getfoundry.sh)
#   - the payer wallet already holds >= the deposit amount in USDFC (faucet it first)
#
# Usage:
#   scripts/staging-fund-payer.sh
#
# Env overrides:
#   OP_ITEM                 1Password item ref  (default: op://Fil One/FilOne Forge Staging)
#   CALIBNET_RPC_URL        Eth JSON-RPC        (default: https://api.calibration.node.glif.io/rpc/v1)
#   OPERATOR_ADDRESS        operator to approve (default: FWSS_ADDRESS from smart-contracts.env)
#   USDFC_DEPOSIT_AMOUNT    USDFC to deposit    (default: 3)
#   USDFC_LOCKUP_ALLOWANCE  operator lockup cap (default: 3, USDFC; must be >= deposit's use)
#   USDFC_RATE_ALLOWANCE    operator rate cap   (default: 0.1, USDFC per epoch)
#   USDFC_MAX_LOCKUP_PERIOD operator period cap (default: 86400 epochs, ~30d at 30s/epoch)
#   FORCE_DEPOSIT=1         deposit even if the account already holds >= the deposit amount
#   CAST_EXTRA_ARGS         extra flags appended to every `cast send` (e.g. --legacy)
set -euo pipefail

OP_ITEM="${OP_ITEM:-op://Fil One/FilOne Forge Staging}"
CALIBNET_RPC_URL="${CALIBNET_RPC_URL:-https://api.calibration.node.glif.io/rpc/v1}"
USDFC_DEPOSIT_AMOUNT="${USDFC_DEPOSIT_AMOUNT:-3}"
USDFC_LOCKUP_ALLOWANCE="${USDFC_LOCKUP_ALLOWANCE:-3}"
USDFC_RATE_ALLOWANCE="${USDFC_RATE_ALLOWANCE:-0.1}"
USDFC_MAX_LOCKUP_PERIOD="${USDFC_MAX_LOCKUP_PERIOD:-86400}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGING="$REPO_ROOT/environments/staging"
CONTRACTS_ENV="$STAGING/smart-contracts.env"
WALLETS_ENV="$STAGING/wallets.env"

# --- Preflight --------------------------------------------------------------
command -v op   >/dev/null || { echo "ERROR: 1Password CLI 'op' not found in PATH" >&2; exit 1; }
command -v cast >/dev/null || { echo "ERROR: foundry 'cast' not found in PATH — see https://getfoundry.sh" >&2; exit 1; }
op whoami >/dev/null 2>&1  || { echo "ERROR: not signed in to 1Password — run 'op signin'" >&2; exit 1; }
[ -f "$CONTRACTS_ENV" ] || { echo "ERROR: $CONTRACTS_ENV not found" >&2; exit 1; }
[ -f "$WALLETS_ENV" ]   || { echo "ERROR: $WALLETS_ENV not found — run 'make staging-keygen' first" >&2; exit 1; }

# smart-contracts.env / wallets.env are committed, simple KEY=value files; source
# them to pull the chain id, contract addresses, and PAYER_ADDRESS.
set -a
# shellcheck disable=SC1090
. "$CONTRACTS_ENV"
# shellcheck disable=SC1090
. "$WALLETS_ENV"
set +a

: "${CHAIN_ID:?CHAIN_ID missing from smart-contracts.env}"
: "${USDFC_TOKEN_ADDRESS:?USDFC_TOKEN_ADDRESS missing from smart-contracts.env}"
: "${FILECOIN_PAY_ADDRESS:?FILECOIN_PAY_ADDRESS missing from smart-contracts.env}"
: "${FWSS_ADDRESS:?FWSS_ADDRESS missing from smart-contracts.env}"
: "${PAYER_ADDRESS:?PAYER_ADDRESS missing from wallets.env — run 'make staging-keygen' and commit it}"
OPERATOR_ADDRESS="${OPERATOR_ADDRESS:-$FWSS_ADDRESS}"

# Guard against pointing at the wrong chain (e.g. a stale/misconfigured RPC).
rpc_chain_id="$(cast chain-id --rpc-url "$CALIBNET_RPC_URL")" \
  || { echo "ERROR: could not reach RPC $CALIBNET_RPC_URL" >&2; exit 1; }
[ "$rpc_chain_id" = "$CHAIN_ID" ] || {
  echo "ERROR: RPC chain id $rpc_chain_id != expected $CHAIN_ID ($CALIBNET_RPC_URL)" >&2
  exit 1
}

# USDFC has 18 decimals, so `ether` (1e18) is the correct unit for cast to-wei.
deposit_wei="$(cast to-wei "$USDFC_DEPOSIT_AMOUNT" ether)"
lockup_allowance_wei="$(cast to-wei "$USDFC_LOCKUP_ALLOWANCE" ether)"
rate_allowance_wei="$(cast to-wei "$USDFC_RATE_ALLOWANCE" ether)"

echo "=== Fund staging payer (Calibnet, chain $CHAIN_ID) ==="
echo "  RPC:          $CALIBNET_RPC_URL"
echo "  Payer:        $PAYER_ADDRESS"
echo "  USDFC token:  $USDFC_TOKEN_ADDRESS"
echo "  FilecoinPay:  $FILECOIN_PAY_ADDRESS"
echo "  Operator:     $OPERATOR_ADDRESS"
echo "  Deposit:      $USDFC_DEPOSIT_AMOUNT USDFC ($deposit_wei)"
echo "  Lockup cap:   $USDFC_LOCKUP_ALLOWANCE USDFC   Rate cap: $USDFC_RATE_ALLOWANCE USDFC/epoch   Max period: $USDFC_MAX_LOCKUP_PERIOD epochs"
echo

# first field of a cast call result, dropping the "[1e18]" scientific annotation.
raw() { awk '{print $1}'; }

wallet_bal="$(cast call "$USDFC_TOKEN_ADDRESS" "balanceOf(address)(uint256)" "$PAYER_ADDRESS" --rpc-url "$CALIBNET_RPC_URL" | raw)"
account_funds="$(cast call "$FILECOIN_PAY_ADDRESS" "accounts(address,address)(uint256,uint256,uint256,uint256)" "$USDFC_TOKEN_ADDRESS" "$PAYER_ADDRESS" --rpc-url "$CALIBNET_RPC_URL" | sed -n '1p' | raw)"
echo "  Wallet USDFC balance:      $(cast from-wei "$wallet_bal" ether) USDFC"
echo "  FilecoinPay account funds: $(cast from-wei "$account_funds" ether) USDFC"
echo

# lt A B — true if non-negative integer A < B. Wei amounts reach ~1e19+, past
# bash's signed-64-bit range, so compare as decimal strings: fewer digits wins,
# else lexicographic (leading zeros stripped first).
lt() {
  local a b
  a="$(printf '%s' "$1" | sed 's/^0*//')"; a="${a:-0}"
  b="$(printf '%s' "$2" | sed 's/^0*//')"; b="${b:-0}"
  if [ "${#a}" -ne "${#b}" ]; then
    [ "${#a}" -lt "${#b}" ]
  else
    [[ "$a" < "$b" ]]
  fi
}

# The payer wallet must hold at least the deposit amount.
if lt "$wallet_bal" "$deposit_wei"; then
  echo "ERROR: payer wallet holds less USDFC than the deposit amount." >&2
  echo "       Faucet it first: https://forest-explorer.chainsafe.dev/faucet/calibnet_usdfc" >&2
  exit 1
fi

# The payer key is read straight from 1Password into memory and used only as a
# cast flag — never echoed, never written to disk. Do NOT enable `set -x`.
PAYER_KEY="$(op read "$OP_ITEM/payer-key")"
case "$PAYER_KEY" in 0x*) ;; *) PAYER_KEY="0x$PAYER_KEY";; esac

# send_tx <fn-label> <done-label> <cast-send args…> — broadcast the transaction
# (async, so we get the hash right away), print it plus a "waiting" note, then
# block on the receipt and print the check mark with status/block/gas. cast never
# echoes the private key. `field NAME` pulls a row out of the receipt table.
field() { awk -v k="$1" '$1==k{$1="";sub(/^ +/,"");print}'; }
send_tx() {
  local fn="$1" done_msg="$2"; shift 2
  local txhash
  if ! txhash="$(cast send --async --rpc-url "$CALIBNET_RPC_URL" --private-key "$PAYER_KEY" ${CAST_EXTRA_ARGS:-} "$@")"; then
    echo "  ❌ $fn failed to submit" >&2
    return 1
  fi
  echo "  📤 transaction submitted: $txhash"
  echo "  ⏳ waiting for on-chain confirmation (Calibnet ~30s/block)…"
  local receipt
  if ! receipt="$(cast receipt "$txhash" --confirmations 1 --rpc-url "$CALIBNET_RPC_URL")"; then
    echo "  ❌ $fn receipt fetch failed (tx $txhash)" >&2
    return 1
  fi
  echo "  ✅ $done_msg   status: $(printf '%s\n' "$receipt" | field status)   block: $(printf '%s\n' "$receipt" | field blockNumber)   gas: $(printf '%s\n' "$receipt" | field gasUsed)"
}

echo "[1/3] approve — let FilecoinPay pull $USDFC_DEPOSIT_AMOUNT USDFC"
send_tx "approve()" approved "$USDFC_TOKEN_ADDRESS" "approve(address,uint256)" "$FILECOIN_PAY_ADDRESS" "$deposit_wei"
echo

echo "[2/3] deposit — credit $USDFC_DEPOSIT_AMOUNT USDFC to the payer's FilecoinPay account"
if ! lt "$account_funds" "$deposit_wei" && [ "${FORCE_DEPOSIT:-0}" != "1" ]; then
  echo "  ⏭  account already holds >= the deposit amount; skipping (set FORCE_DEPOSIT=1 to deposit anyway)"
else
  send_tx "deposit()" deposited "$FILECOIN_PAY_ADDRESS" "deposit(address,address,uint256)" "$USDFC_TOKEN_ADDRESS" "$PAYER_ADDRESS" "$deposit_wei"
fi
echo

echo "[3/3] setOperatorApproval — let warm-storage ($OPERATOR_ADDRESS) lock up funds"
send_tx "setOperatorApproval()" "operator approved" "$FILECOIN_PAY_ADDRESS" \
  "setOperatorApproval(address,address,bool,uint256,uint256,uint256)" \
  "$USDFC_TOKEN_ADDRESS" "$OPERATOR_ADDRESS" true \
  "$rate_allowance_wei" "$lockup_allowance_wei" "$USDFC_MAX_LOCKUP_PERIOD"

echo
account_funds="$(cast call "$FILECOIN_PAY_ADDRESS" "accounts(address,address)(uint256,uint256,uint256,uint256)" "$USDFC_TOKEN_ADDRESS" "$PAYER_ADDRESS" --rpc-url "$CALIBNET_RPC_URL" | sed -n '1p' | raw)"
echo "Done. FilecoinPay account funds now: $(cast from-wei "$account_funds" ether) USDFC"
echo "Re-run 'make staging-deploy-piri' — proof-set creation should now succeed."
