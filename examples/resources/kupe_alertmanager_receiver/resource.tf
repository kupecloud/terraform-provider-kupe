# A Slack receiver. body_json takes Alertmanager's receiver schema; see
# https://prometheus.io/docs/alerting/latest/configuration/#receiver
# for every block it supports (slack_configs, email_configs,
# webhook_configs, pagerduty_configs, msteams_configs and others).
#
# jsonencode() handles the escaping.
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

# The credentials come from sensitive variables, which keeps them out of your
# configuration files. They are still stored in Terraform state in plain
# text, as part of body_json.
variable "slack_webhook_url" {
  type      = string
  sensitive = true
}

variable "pagerduty_service_key" {
  type      = string
  sensitive = true
}
