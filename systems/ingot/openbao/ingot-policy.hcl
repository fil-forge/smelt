# Policy for ingot's OpenBao token: wrap, unwrap, and rewrap CEKs under the
# region KEK, and nothing else. The key itself (read, rotate, export, config)
# stays out of reach; export is impossible anyway (exportable=false).
path "transit/encrypt/region-kek" {
  capabilities = ["update"]
}

path "transit/decrypt/region-kek" {
  capabilities = ["update"]
}

path "transit/rewrap/region-kek" {
  capabilities = ["update"]
}
