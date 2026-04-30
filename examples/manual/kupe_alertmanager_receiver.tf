# Smoke test for kupe_alertmanager_receiver.
#
# Verifies:
#   - 201/200 from PUT /alertmanager/receivers/<name>
#   - kupe-api accepts the slack_configs schema
#   - operator pushes the receiver into Mimir's alertmanager config
#     (verify with `kubectl logs -n mimir mimir-alertmanager-0 |
#     grep smoke-receiver` after apply)

resource "kupe_alertmanager_receiver" "smoke" {
  name = "smoke-receiver"
  body_json = jsonencode({
    slack_configs = [{
      api_url       = var.slack_webhook_url
      channel       = "#smoke-test"
      send_resolved = true
      title         = "[{{ .Status | toUpper }}] {{ .CommonLabels.alertname }}"
    }]
  })
}
