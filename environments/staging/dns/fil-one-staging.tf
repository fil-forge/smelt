# Forge staging DNS — REFERENCE COPY.
#
# This is not applied from the smelt repo. Copy these resource definitions into
#   fil-one/infrastructure → environments/staging/fil-one.tf
# which already defines `local.zone_id` (data.cloudflare_zone.filone, "fil.one").
#
# A records for the Forge staging services fronted by host Caddy on the
# Servers.com Calibnet box (root@23.83.66.244). proxied = false (DNS-only) so
# Caddy's ACME HTTP-01 challenge reaches the host directly and did:web resolution
# (https://<host>/.well-known/did.json) sees the real origin.

locals {
  forge_staging_box_ipv4 = "23.83.66.244"

  # <label>.fil.one, i.e. sprue.staging.fil.one, signing-service.staging.fil.one, ...
  forge_staging_hosts = [
    "sprue.staging",           # upload service (sprue) — did:web:sprue.staging.fil.one
    "signing-service.staging", # did:web:signing-service.staging.fil.one
    "delegator.staging",       # did:web:delegator.staging.fil.one
    "piri-0.staging",          # piri node public endpoint
  ]
}

resource "cloudflare_record" "forge_staging" {
  for_each = toset(local.forge_staging_hosts)

  zone_id = local.zone_id
  name    = each.value
  type    = "A"
  content = local.forge_staging_box_ipv4
  proxied = false
}
