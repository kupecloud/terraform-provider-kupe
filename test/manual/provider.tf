# Manual smoke test workspace for the kupe terraform provider.
#
# Each `<resource_type>.tf` in this directory defines a single resource
# with the label `smoke`, so any of them can be targeted individually:
#
#   tofu apply -target=kupe_cluster.smoke
#
# Setup
#
#   1. Build and dev-override the local provider:
#        cd ../..               # repo root
#        make local-provider
#        export TF_CLI_CONFIG_FILE="$PWD/.tmp/tfdevrc"
#
#   2. Point the provider at the target API + tenant. The kupe provider
#      reads these from the environment, so nothing host- or tenant-
#      specific is committed here:
#        export KUPE_HOST=https://api.<your-env>.kupe.cloud
#        export KUPE_TENANT=<your-test-tenant>
#        export KUPE_API_KEY=kupe_...
#
#      For Kupe internal dev, see the runbook in the kupe-tests repo
#      for the dev host and the test-tenant fixture.
#
# Run
#
#   # No `tofu init` — the dev override loads the local binary directly.
#   tofu apply -target=<resource_type>.smoke -auto-approve
#   tofu destroy -auto-approve
#
# This is NOT a CI-runnable suite. It's the human-driven smoke step
# before tagging a provider release. See ../../docs/testing.md.

terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}

# host, tenant, and api_key all read from KUPE_HOST / KUPE_TENANT /
# KUPE_API_KEY. Keep this block empty so no environment-specific URL or
# tenant name is committed to a public repo.
provider "kupe" {}
