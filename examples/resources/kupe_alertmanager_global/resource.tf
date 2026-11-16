# The tenant's Alertmanager global section: SMTP defaults, slack_api_url,
# resolve_timeout and the other top-level fields every receiver shares.
# A tenant has one, so declare this resource once.
#
# See https://prometheus.io/docs/alerting/latest/configuration/#configuration-file
# for the complete field list.
resource "kupe_alertmanager_global" "main" {
  body_json = jsonencode({
    smtp_from          = "alerts@example.com"
    smtp_smarthost     = "smtp.example.com:587"
    smtp_auth_username = "alerts@example.com"
    smtp_auth_password = var.smtp_password
    resolve_timeout    = "5m"
  })
}

variable "smtp_password" {
  type      = string
  sensitive = true
}
