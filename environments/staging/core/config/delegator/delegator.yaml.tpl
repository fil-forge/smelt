# delegator config — STAGING TEMPLATE.
#
# Rendered with `op inject` into $FORGE_SECRETS_DIR/delegator.yaml and mounted at
# /.delegator.yaml. The only secret is contract.transactor.key (a 1Password reference). The
# delegator identity is a mounted key file; the delegation proofs are committed
# and mounted from the git tree at /proofs.
#
# Rendered with `op inject` (the transactor key) followed by ${VAR} substitution
# from the shared smart-contracts.env (chain id, RPC URL, contract addresses) — so no
# address is duplicated here.
#
# NOTE: the delegator validates an indexing-service and egress-tracking-service
# delegation at startup even though neither service runs in staging — that's why
# those DIDs + proof files are still required here.

server:
  host: "0.0.0.0"
  port: 80

store:
  region: "us-east-1"
  allowlist_table_name: "delegator-allow-list"
  providerinfo_table_name: "delegator-provider-info"
  providerweight: 1
  endpoint: "http://dynamodb-local:8000"

delegator:
  key_file: "/keys/delegator.pem"
  did: "did:web:delegator.staging.fil.one"
  indexing_service_web_did: "did:web:indexer.staging.fil.one"
  indexing_service_proof_file: "/proofs/indexing-service-proof.txt"
  egress_tracking_service_did: "did:web:etracker.staging.fil.one"
  egress_tracking_service_proof_file: "/proofs/egress-tracking-proof.txt"
  upload_service_did: "did:web:sprue.staging.fil.one"

contract:
  chain_client_endpoint: "${LOTUS_RPC_URL}"
  payments_contract_address: "${FILECOIN_PAY_ADDRESS}"
  service_contract_address: "${FWSS_ADDRESS}"
  registry_contract_address: "${SERVICE_PROVIDER_REGISTRY_ADDRESS}"
  transactor:
    chain_id: ${CHAIN_ID}
    key: "{{ op://Fil One/FilOne Forge Staging/delegator-transactor-key }}"
