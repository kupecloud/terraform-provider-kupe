# A Slack notification receiver. The body_json field accepts the standard
# Alertmanager receiver schema — see
# https://prometheus.io/docs/alerting/latest/configuration/#receiver
# for the full set of supported sub-blocks (slack_configs, email_configs,
# webhook_configs, pagerduty_configs, msteams_configs, etc.).
#
# Use jsonencode() so HCL handles escaping for you.
resource "kupe_alertmanager_receiver" "slack" {
  name = "slack"
  body_json = jsonencode({
    slack_configs = [{
      api_url       = var.slack_webhook_url
      channel       = "#alerts"
      send_resolved = true
      title         = "[{{ .Status | toUpper }}] {{ .CommonLabels.alertname }}"
      text          = "{{ range .Alerts }}{{ .Annotations.summary }}\n{{ end }}"
    }]
  })
}

# A PagerDuty receiver for paging on critical alerts.
resource "kupe_alertmanager_receiver" "pagerduty" {
  name = "pagerduty"
  body_json = jsonencode({
    pagerduty_configs = [{
      service_key   = var.pagerduty_service_key
      send_resolved = true
    }]
  })
}

# Sensitive inputs come from variables so the state file does not contain
# secrets. The Mimir gateway never sees the variable contents in plaintext —
# kupe-api validates the receiver, then forwards it to Mimir's storage layer.
variable "slack_webhook_url" {
  type      = string
  sensitive = true
}

variable "pagerduty_service_key" {
  type      = string
  sensitive = true
}
