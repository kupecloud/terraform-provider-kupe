# The Kupe platform creates the tenant's registry when the plan includes one.
# Bring it under Terraform instead of creating it.
import {
  to = kupe_registry.this
  id = "registry"
}
