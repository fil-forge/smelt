# ingot-openbao server config (local dev).
#
# The regional OpenBao that holds ingot's region KEK (regional security RFC):
# a non-dev `bao server` with integrated raft storage on the
# ingot-openbao-data volume, so the KEK and every wrapped CEK survive
# `make down` / `make up` and snapshot restores. ingot-openbao-init
# initializes, unseals, and provisions it on every boot.
#
# The seal is the RFC's shape: `seal "transit"` against central-openbao.
# At boot this server presents its seal token (BAO_TOKEN in compose.yml),
# central unwraps the barrier key, and the server unseals; with central
# unreachable or the token revoked it does not start. Production differs
# only in the listener (a unix socket) and TLS towards central.

ui = false

api_addr     = "http://ingot-openbao:8200"
cluster_addr = "http://ingot-openbao:8201"

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}

storage "raft" {
  path    = "/openbao/file"
  node_id = "ingot-openbao"
}

seal "transit" {
  address    = "http://central-openbao:8200"
  key_name   = "ingot-openbao"
  mount_path = "seal/"
  # token: BAO_TOKEN from the environment (compose.yml); the OpenBao docs
  # recommend the environment over this file for it.
}
