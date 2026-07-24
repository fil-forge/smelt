# Core bundle — SECRET environment values (TEMPLATE).
#
# Rendered on a developer machine with:
#   op inject -i secrets.env.tpl   (streamed to the box, never written locally)
# producing secrets.env in $FORGE_SECRETS_DIR, consumed via
#   docker compose --env-file secrets.env
#
# NEVER commit the rendered secrets.env. Only this template (with 1Password references) is
# tracked. Values resolve from the single 1Password item "FilOne Forge Staging" (vault "Fil One").
#
# Note: op inject scans the entire file, comments included — so never write a bare
# 1Password reference URL or a template-brace token in a comment; it tries to resolve
# them and fails the whole render.

# Postgres. One shared instance: the admin superuser initializes the cluster;
# postgres-init creates one role + database per service. SPRUE_POSTGRES_PASSWORD
# must match the password baked into sprue's DSN (rendered sprue-config.yaml).
POSTGRES_ADMIN_PASSWORD={{ op://Fil One/FilOne Forge Staging/core-postgres-admin-password }}
SPRUE_POSTGRES_PASSWORD={{ op://Fil One/FilOne Forge Staging/sprue-postgres-password }}
HILT_POSTGRES_PASSWORD={{ op://Fil One/FilOne Forge Staging/hilt-postgres-password }}
PLC_POSTGRES_PASSWORD={{ op://Fil One/FilOne Forge Staging/plc-postgres-password }}

# MinIO root credentials. The minio + minio-init containers authenticate with
# these; sprue receives the same key/secret through its rendered config file
# (storage.s3 in sprue-config.yaml), not via these env vars.
MINIO_ROOT_USER={{ op://Fil One/FilOne Forge Staging/minio-access-key }}
MINIO_ROOT_PASSWORD={{ op://Fil One/FilOne Forge Staging/minio-secret-key }}

# Hilt: pre-shared bearer token for the Tenant API (operators `op read` the same
# field for curl calls). The hilt Vault credentials live in a separate file
# (vault-secrets.env), rendered by `make staging-vault-init` — they are minted at
# runtime by `vault operator init`, not offline like the fields above.
HILT_PARTNER_KEY={{ op://Fil One/FilOne Forge Staging/hilt-partner-key }}
