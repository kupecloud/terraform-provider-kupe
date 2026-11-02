resource "kupe_app" "this" {
  name = "shop"

  # Protection defaults to true. Before destroy, set this to false and apply.
  # deletion_protection = false
}
