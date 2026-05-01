# Placeholder variables for the alertmanager smoke tests. kupe-api
# only schema-validates these — the URLs/credentials are never actually
# called during the smoke (Mimir's alertmanager doesn't fire test
# notifications during a routing/global config reload).

variable "slack_webhook_url" {
  description = "Placeholder Slack webhook URL for the receiver smoke."
  type        = string
  sensitive   = true
  default     = "https://hooks.slack.com/services/SMOKE/TEST/PLACEHOLDER"
}

variable "smtp_password" {
  description = "Placeholder SMTP password for the global smoke."
  type        = string
  sensitive   = true
  default     = "smoke-placeholder"
}
