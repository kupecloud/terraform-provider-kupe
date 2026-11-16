resource "kupe_app" "this" {
  name = "shop"

  # Deletion protection is on by default. To destroy the app, set this to
  # false and apply first.
  # deletion_protection = false
}
