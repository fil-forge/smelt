# hilt-vault server config (local dev).
#
# The OpenBao that holds hilt's tenant and access-key private keys: a non-dev
# `bao server` with integrated raft storage on the hilt-vault-data volume, so
# the keys survive `make down` / `make up` and snapshot restores alongside
# the tenant and access-key rows in hilt-postgres. hilt-vault-init
# initializes, unseals, and provisions it on every boot.

ui = false

api_addr     = "http://hilt-vault:8200"
cluster_addr = "http://hilt-vault:8201"

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}

storage "raft" {
  path    = "/openbao/file"
  node_id = "hilt-vault"
}
