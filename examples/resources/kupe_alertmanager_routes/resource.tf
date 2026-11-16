# The tenant's route list. The platform manages the root route, with its
# default receiver and group_by; this resource owns the ordered child routes
# under it. A tenant has one list, so declare this resource once.
#
# Routes are checked top to bottom and the first match wins, unless the
# matched route sets `continue`. Because this resource owns the whole list,
# reordering, adding or removing routes is a single update.

# The two receivers the routes below send to, included so the example
# stands alone. The API refuses routes that name a receiver that does not
# exist, so a real configuration declares its receivers too.
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
    # The infra team's alerts go to Slack only.
    {
      matchers = ["team=\"infra\""]
      receiver = "slack"
    },
    # Warnings go to the default receiver: with no `receiver`, the route
    # inherits the root route's. `continue = true` keeps matching later routes.
    {
      matchers = ["severity=\"warning\""]
      continue = true
    },
  ])

  # The routes name their receivers as strings, so Terraform cannot see the
  # dependency. depends_on creates the receivers first: without it, Terraform
  # can write the routes first, and the API refuses a route whose receiver
  # does not exist yet.
  depends_on = [
    kupe_alertmanager_receiver.slack,
    kupe_alertmanager_receiver.pagerduty,
  ]
}
