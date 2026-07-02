# Piri base config — STAGING TEMPLATE (no secrets).
#
# Rendered at provision time by substituting ${VAR} from environments/staging/
# smart-contracts.env (contract addresses, chain id) and environments/staging/
# wallets.env (the keygen-written PAYER_ADDRESS), then shipped to the box and
# merged by `piri init`. No address is duplicated — those two files are the
# single source.

[pdp]
chain_id = "${CHAIN_ID}"
# The funded signing-service payer address (written into wallets.env by
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

# The indexer/IPNI are NOT deployed in staging, so the integration is disabled:
# omitting [ucan.services.indexer] and ipni_announce_urls makes piri skip claim
# caching and IPNI announcements (blob/accept would otherwise fail hard trying
# to POST to the indexer). Requires piri with optional-indexer support.
[ucan.services.upload]
did = "did:web:sprue.staging.fil.one"
url = "https://sprue.staging.fil.one"
