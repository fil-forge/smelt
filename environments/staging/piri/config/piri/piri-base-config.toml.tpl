# Piri base config — STAGING TEMPLATE (no secrets).
#
# Rendered at provision time by substituting ${VAR} from the shared
# environments/staging/smart-contracts.env (contract addresses, chain id, and the
# keygen-written PAYER_ADDRESS), then shipped to the box and merged by
# `piri init`. No address is duplicated — smart-contracts.env is the single source.

[pdp]
chain_id = "${CHAIN_ID}"
# The funded signing-service payer address (written into smart-contracts.env by
# `smelt staging keygen`).
payer_address = "${PAYER_ADDRESS}"

[pdp.signing_service]
did = "did:web:signing-service.staging.fil.one"
url = "https://signing-service.staging.fil.one"

[pdp.contracts]
verifier = "${PDP_VERIFIER_ADDRESS}"
provider_registry = "${SERVICE_PROVIDER_REGISTRY_ADDRESS}"
service = "${FWSS_ADDRESS}"
service_view = "${FWSS_VIEW_ADDRESS}"
payments = "${FILECOIN_PAY_ADDRESS}"
usdfc_token = "${USDFC_TOKEN_ADDRESS}"

# The indexer/IPNI are NOT deployed in staging. These sections are kept (pointing
# at non-resolving staging identities) only so `piri init` accepts the config;
# piri's claim publishing/announce calls will no-op. Remove if piri tolerates it.
[ucan.services.indexer]
did = "did:web:indexer.staging.fil.one"
url = "https://indexer.staging.fil.one/claims"

[ucan.services.upload]
did = "did:web:sprue.staging.fil.one"
url = "https://sprue.staging.fil.one"

[ucan.services.publisher]
ipni_announce_urls = ["https://indexer.staging.fil.one/announce"]
