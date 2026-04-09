# Singleton-per-tenant Alertmanager global section. Use this to set SMTP
# defaults, slack_api_url, resolve_timeout, and other top-level fields shared
# by every receiver in the tenant.
#
# See https://prometheus.io/docs/alerting/latest/configuration/#configuration-file
# for the complete field list.
resource "kupe_alertmanager_global" "main" {
  body_json = jsonencode({
    smtp_from         = "alerts@example.com"
    smtp_smarthost    = "smtp.example.com:587"
    smtp_auth_username = "alerts@example.com"
    smtp_auth_password = var.smtp_password
    resolve_timeout   = "5m"
  })
}

variable "smtp_password" {
  type      = string
  sensitive = true
}
