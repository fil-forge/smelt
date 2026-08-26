# ingot-openbao server config (local dev).
#
# The regional OpenBao that holds ingot's region KEK (regional security RFC):
# a non-dev `bao server` with integrated raft storage on the
# ingot-openbao-data volume, so the KEK and every wrapped CEK survive
# `make down` / `make up` and snapshot restores. ingot-openbao-init
# initializes, unseals, and provisions it on every boot.
#
# Production differs in two places the RFC spells out: the listener is a
# unix socket, and the seal is `seal "transit"` against a central OpenBao
# instead of a stored unseal key. Both are config changes to this file.

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
