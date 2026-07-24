# HashiCorp Vault server config for staging hilt-vault — committed, NON-SECRET.
#
# Replaces dev mode: a persistent, properly-sealed Vault using the integrated
# Raft storage backend. Data lives on the box's ZFS pool (bind-mounted at
# /vault/file); the hilt-vault-unseal sidecar unseals it on every (re)start with
# the key held in 1Password. See docs/STAGING_DEPLOY.md ("Persistent sealed
# Vault") and scripts/staging-vault-init.sh (the one-time init ceremony).

# /vault/file is the image's "blessed" data path: the stock docker-entrypoint.sh
# chowns it to the `vault` user on startup (when the container starts as root)
# before stepping down, so a root-owned host bind mount becomes writable without
# any `user:` override or host-side chown.
storage "raft" {
  path    = "/vault/file"
  node_id = "hilt-vault-0"
}

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = 1
}

# api_addr/cluster_addr are required by Raft. Container-internal DNS name; Vault
# is not published to the host in staging (network-internal only).
api_addr     = "http://hilt-vault:8200"
cluster_addr = "http://hilt-vault:8201"

# Recommended with integrated storage: Raft manages its own on-disk encryption,
# and disabling mlock avoids the IPC_LOCK/swap-lock requirement.
disable_mlock = true

ui = false
