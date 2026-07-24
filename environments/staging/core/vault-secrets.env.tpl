# Core bundle — hilt VAULT secret values (TEMPLATE).
#
# Separate from secrets.env because these two values have a different lifecycle:
# they are minted at RUNTIME by `vault operator init` (not offline by keygen) and
# stored into 1Password by `make staging-vault-init`, which then renders and ships
# this file. secrets.env is rendered earlier (by staging-provision-core, before
# init has run), so these fields can't live there.
#
# Rendered on a developer machine and streamed to the box, never written locally.
# Consumed via `docker compose --env-file vault-secrets.env` alongside secrets.env.
#
# Note: op inject scans the entire file, comments included — so never write a bare
# 1Password reference URL or a template-brace token in a comment.

# The Vault root token (from `operator init`) doubles as hilt's client token; the
# hilt-vault-unseal sidecar uses the unseal key to unseal Vault on every restart.
HILT_VAULT_TOKEN={{ op://Fil One/FilOne Forge Staging/hilt-vault-root-token }}
HILT_VAULT_UNSEAL_KEY={{ op://Fil One/FilOne Forge Staging/hilt-vault-unseal-key }}
