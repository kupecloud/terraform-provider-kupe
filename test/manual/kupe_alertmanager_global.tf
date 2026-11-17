# Smoke test for kupe_alertmanager_global (singleton per tenant).
#
# Verifies:
#   - 200 from PUT /alertmanager/global
#   - schema validation accepts slack_api_url
#   - update path: changing resolve_timeout triggers PUT (not recreate)

resource "kupe_alertmanager_global" "smoke" {
  body_json = jsonencode({
    slack_api_url   = var.slack_webhook_url
    resolve_timeout = "5m"
  })
}
