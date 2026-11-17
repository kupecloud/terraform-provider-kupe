# The tenant's Alertmanager global section: slack_api_url,
# resolve_timeout and the other top-level fields every receiver shares.
# A tenant has one, so declare this resource once.
#
# See https://prometheus.io/docs/alerting/latest/configuration/#configuration-file
# for the complete field list.
resource "kupe_alertmanager_global" "main" {
  body_json = jsonencode({
    slack_api_url   = var.slack_webhook_url
    resolve_timeout = "5m"
  })
}

variable "slack_webhook_url" {
  type      = string
  sensitive = true
}
