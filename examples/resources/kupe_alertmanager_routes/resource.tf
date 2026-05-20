# Singleton-per-tenant route list. The root route itself (default receiver,
# default group_by) is implicit and managed by kupe-api; this resource owns
# the ordered list of child routes that branch off the root.
#
# Routes are evaluated top-to-bottom — the first match wins (unless `continue`
# is set on the matched route). Reordering, inserting, and removing routes are
# all atomic single-PUT operations because the resource owns the entire list.

# Two receivers the routes below dispatch to. Included inline so the example
# stands alone — the kupe-api validator rejects route configs that reference
# undeclared receivers, so any real config will need at least one receiver
# resource alongside the routes resource.
resource "kupe_alertmanager_receiver" "slack" {
  name = "slack"
  body_json = jsonencode({
    slack_configs = [{
      api_url = var.slack_webhook_url
      channel = "#alerts"
    }]
  })
}

resource "kupe_alertmanager_receiver" "pagerduty" {
  name = "pagerduty"
  body_json = jsonencode({
    pagerduty_configs = [{
      service_key = var.pagerduty_service_key
    }]
  })
}

variable "slack_webhook_url" {
  type      = string
  sensitive = true
}

variable "pagerduty_service_key" {
  type      = string
  sensitive = true
}

resource "kupe_alertmanager_routes" "main" {
  routes_json = jsonencode([
    # Page on critical severity, day or night.
    {
      matchers        = ["severity=\"critical\""]
      receiver        = "pagerduty"
      group_wait      = "10s"
      group_interval  = "5m"
      repeat_interval = "1h"
    },
    # Slack-only for the infra team.
    {
      matchers = ["team=\"infra\""]
      receiver = "slack"
    },
    # Catch-all for everything else — fall through to the default receiver
    # by leaving `receiver` empty (inherits from the root).
    {
      matchers = ["severity=\"warning\""]
      continue = true
    },
  ])

  # Make sure the receivers exist before this resource creates the routes.
  # Without these explicit dependencies, terraform may try to write the
  # routes first and the kupe-api validator will reject any references to
  # missing receivers with a 400.
  depends_on = [
    kupe_alertmanager_receiver.slack,
    kupe_alertmanager_receiver.pagerduty,
  ]
}
