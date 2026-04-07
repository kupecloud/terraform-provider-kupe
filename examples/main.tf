terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}

provider "kupe" {
  host   = "https://api.kupe.cloud"
  tenant = "acme"
  # api_key via KUPE_API_KEY env var
}

# Read current tenant info
data "kupe_tenant" "current" {}

# Read the pro plan details
data "kupe_plan" "pro" {
  name = "pro"
}

# Create a production cluster
resource "kupe_cluster" "production" {
  name         = "production"
  display_name = "Production"
  type         = "shared"
  version      = "1.31"

  resources = {
    cpu     = "4"
    memory  = "16Gi"
    storage = "100Gi"
  }
}

# Create a staging cluster
resource "kupe_cluster" "staging" {
  name         = "staging"
  display_name = "Staging"
  type         = "shared"
  version      = "1.31"

  resources = {
    cpu     = "2"
    memory  = "8Gi"
    storage = "50Gi"
  }
}

# Sync a database password to production
resource "kupe_secret" "db_password" {
  name        = "db-password"
  secret_path = "production/db-password"

  sync = [
    {
      cluster   = kupe_cluster.production.name
      namespace = "default"
    },
    {
      cluster     = kupe_cluster.production.name
      namespace   = "backend"
      secret_name = "database-credentials"
    },
  ]
}

# Add a team member
resource "kupe_tenant_member" "developer" {
  email = "dev@acme.com"
  role  = "readonly"
}

# Create a CI/CD API key
resource "kupe_api_key" "cicd" {
  display_name = "CI/CD Pipeline"
  role         = "admin"
  expires_at   = "2027-01-01T00:00:00Z"
}

output "cicd_api_key" {
  value     = kupe_api_key.cicd.key
  sensitive = true
}

output "production_endpoint" {
  value = kupe_cluster.production.endpoint
}
