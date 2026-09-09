# central-openbao server config (local dev).
#
# The central OpenBao of the regional security RFC: it holds the transit
# keys that seal each appliance's local OpenBao (ingot-openbao here). At
# boot an appliance presents its token, central unwraps the appliance's
# barrier key, and the appliance unseals; revoking that token at central is
# the startup-kill lever. This instance holds nothing else: hilt's tenant
# keys stay in hilt-vault.
#
# Production differs in the root of trust for this instance (HSM/KMS unseal
# instead of a stored share), TLS on the listener, and CIDR-bound appliance
# tokens. All of those are changes to this file or to init.sh.

ui = false

api_addr     = "http://central-openbao:8200"
cluster_addr = "http://central-openbao:8201"

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}

storage "raft" {
  path    = "/openbao/file"
  node_id = "central-openbao"
}
