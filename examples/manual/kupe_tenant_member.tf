# Smoke test for kupe_tenant_member.
#
# Verifies:
#   - 201 from POST /members
#   - member appears in GET /members and Tenant CR's spec.members:
#       kubectl describe tenant kupe-test -n tenant-kupe-test
#   - update path: changing role triggers PATCH not recreate
#
# Use a synthetic email so a leaked test add doesn't accidentally invite
# a real account.

resource "kupe_tenant_member" "smoke" {
  email = "smoke-test-member@kupe.cloud"
  role  = "readonly"
}
