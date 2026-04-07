resource "kupe_api_key" "cicd" {
  display_name = "CI/CD Pipeline"
  role         = "admin"
  expires_at   = "2027-01-01T00:00:00Z"
}
