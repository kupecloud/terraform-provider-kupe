resource "kupe_tenant_member" "developer" {
  email = "dev@example.com"
  role  = "readonly"
}
