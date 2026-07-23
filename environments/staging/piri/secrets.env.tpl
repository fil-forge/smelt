# Piri bundle — SECRET environment values (TEMPLATE).
#
# Rendered on a developer machine with:
#   op inject -i secrets.env.tpl   (streamed to the box, never written locally)
# producing piri-secrets.env in $FORGE_SECRETS_DIR (a distinct basename — the
# core bundle owns secrets.env), consumed via
#   docker compose --env-file $FORGE_SECRETS_DIR/piri-secrets.env
#
# NEVER commit the rendered piri-secrets.env. Only this template (with 1Password
# references) is tracked. Values resolve from the single 1Password item
# "FilOne Forge Staging" (vault "Fil One").
#
# Note: op inject scans the entire file, comments included — so never write a bare
# 1Password reference URL or a template-brace token in a comment; it tries to resolve
# them and fails the whole render.

# Postgres. One shared instance: the admin superuser initializes the cluster;
# postgres-init creates one role + database per service (piri_0, ingot).
# INGOT_POSTGRES_PASSWORD must match the password baked into ingot's DSN
# (rendered ingot-config.yaml).
POSTGRES_ADMIN_PASSWORD={{ op://Fil One/FilOne Forge Staging/piri-postgres-admin-password }}
PIRI_0_POSTGRES_PASSWORD={{ op://Fil One/FilOne Forge Staging/piri-0-postgres-password }}
INGOT_POSTGRES_PASSWORD={{ op://Fil One/FilOne Forge Staging/ingot-postgres-password }}
