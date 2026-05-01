# Smoke test for kupe_plan data source.
#
# Reads the starter plan (which is what the kupe-test tenant fixture
# subscribes to). Returns display name, platform fee, max clusters, and
# resource pool limits.
#
# Verifies:
#   - GET /api/v1/plans/starter responds 200
#   - the plan exists on dev (apply the kupe-test fixture before running)

data "kupe_plan" "smoke" {
  name = "starter"
}

output "smoke_plan_display_name" {
  value = data.kupe_plan.smoke.display_name
}

output "smoke_plan_max_clusters" {
  value = data.kupe_plan.smoke.max_clusters
}
