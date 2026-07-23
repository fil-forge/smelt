# ingot (S3 gateway) config — STAGING TEMPLATE.
#
# Rendered with `op inject` into $FORGE_SECRETS_DIR/ingot-config.yaml and mounted
# at /etc/ingot/config.yaml. The root S3 credentials and the Postgres password
# are secrets (1Password references); everything else is literal.

# S3 listener. Caddy fronts https://ingot.staging.fil.one → 127.0.0.1:15130.
# PATH-STYLE ONLY: there is no wildcard *.ingot.staging.fil.one DNS/TLS, so
# clients must set force_path_style / addressing_style=path.
addr: "0.0.0.0:9000"
# Must match the region hilt registers ingot under (staging-register-ingot.sh)
# and the AWS_REGION S3 clients sign with.
region: us-west-1
data_dir: /data
log_level: info

# Root S3 account (break-glass; tenant credentials are minted by hilt).
root_access: "{{ op://Fil One/FilOne Forge Staging/ingot-root-access-key }}"
root_secret: "{{ op://Fil One/FilOne Forge Staging/ingot-root-secret-key }}"

# Agent identity (the signer that invokes against sprue and hilt). Ingot acts
# under this key's did:key — it serves no did:web document.
identity:
  key_file: /keys/ingot.pem

# Registry / segment metadata — the piri bundle's shared Postgres (role +
# database created by postgres-init).
postgres_dsn: "postgres://ingot:{{ op://Fil One/FilOne Forge Staging/ingot-postgres-password }}@postgres:5432/ingot?sslmode=disable"

# Forge upload service (sprue) — core bundle, over public https (bundles never
# talk over Docker DNS).
upload_service_url: "https://sprue.staging.fil.one"
upload_service_did: "did:web:sprue.staging.fil.one"
upload_receipts_url: "https://sprue.staging.fil.one/receipt"

# NOTE: no indexer keys — staging runs no indexing-service, and ingot does not
# need one: reads resolve via its local blob_locations registry (LocalLocator);
# the indexer_endpoint/indexer_did keys don't even exist in ingot's Config.

# S3 authorization service (hilt, core bundle); the DID is used verbatim (no
# resolution).
auth_service_url: "https://hilt.staging.fil.one"
auth_service_did: "did:web:hilt.staging.fil.one"
# hilt → ingot delegations for /s3/request/authorize and /s3/bucket/*
# (committed environments/staging/proofs/hilt-ingot-s3-proof.txt).
auth_service_proofs: "/proofs/hilt-ingot-s3-proof.txt"
