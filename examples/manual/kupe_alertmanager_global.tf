# Smoke test for kupe_alertmanager_global (singleton per tenant).
#
# Verifies:
#   - 200 from PUT /alertmanager/global
#   - schema validation accepts the SMTP fields
#   - update path: changing resolve_timeout triggers PUT (not recreate)

resource "kupe_alertmanager_global" "smoke" {
  body_json = jsonencode({
    smtp_from          = "smoke@kupe.cloud"
    smtp_smarthost     = "smtp.example.com:587"
    smtp_auth_username = "smoke@kupe.cloud"
    smtp_auth_password = var.smtp_password
    resolve_timeout    = "5m"
  })
}
