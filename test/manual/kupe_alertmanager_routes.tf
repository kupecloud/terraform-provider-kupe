# Smoke test for kupe_alertmanager_routes (singleton per tenant).
#
# Verifies:
#   - 200 from PUT /alertmanager/routes (whole list replace)
#   - kupe-api validates that every route's `receiver` reference resolves
#     (this is why we depends_on the receiver smoke)
#   - removing all routes replaces with an empty list (apply with the
#     resource removed from state to confirm)

resource "kupe_alertmanager_routes" "smoke" {
  routes_json = jsonencode([
    {
      matchers = ["severity=\"critical\""]
      receiver = "smoke-receiver"
    },
  ])

  depends_on = [kupe_alertmanager_receiver.smoke]
}
