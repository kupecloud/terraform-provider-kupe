# Smoke test for kupe_api_key.
#
# Verifies:
#   - 201 from POST /apikeys
#   - the returned `key` field starts with "kupe_" and is sensitive in state
#   - GET /apikeys lists this key (without the raw value)
#   - destroy invalidates the key (try `curl -H "Authorization: Bearer
#     <output>" https://api.dev.int.kupe.cloud/api/v1/tenants/kupe-test`
#     after destroy and expect 401)

resource "kupe_api_key" "smoke" {
  display_name = "smoke-test-key"
  role         = "readonly"
}

output "smoke_api_key_id" {
  value = kupe_api_key.smoke.id
}

output "smoke_api_key_value" {
  value     = kupe_api_key.smoke.key
  sensitive = true
}
