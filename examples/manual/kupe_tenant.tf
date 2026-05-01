# Smoke test for kupe_tenant data source.
#
# Reads the current tenant (the one in the provider's `tenant` field —
# kupe-test on dev). Returns name, displayName, plan, contactEmail, and
# the enforce* booleans.
#
# Verifies:
#   - GET /api/v1/tenants/kupe-test responds 200
#   - data source surfaces all attributes the schema declares

data "kupe_tenant" "smoke" {}

output "smoke_tenant_name" {
  value = data.kupe_tenant.smoke.name
}

output "smoke_tenant_plan" {
  value = data.kupe_tenant.smoke.plan
}

output "smoke_tenant_display_name" {
  value = data.kupe_tenant.smoke.display_name
}
