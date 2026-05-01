# Manual smoke test workspace for the kupe terraform provider.
#
# Apply against the kupe-test tenant on dev. Each `<resource_type>.tf`
# in this directory defines a single resource with the label `smoke`,
# so any of them can be targeted individually:
#
#   tofu apply -target=kupe_cluster.smoke
#
# Setup
#
#   1. Build and dev-override the local provider:
#        cd ../..               # repo root
#        make local-provider
#
#   2. Apply the kupe-test tenant fixture once per cluster:
#        kubectl --context=<env> apply -f \
#          https://raw.githubusercontent.com/kupecloud/kupe-tests/main/fixtures/tenants/kupe-test.yaml
#
#   3. Mint an admin API key on kupe-test (one-time), export it:
#        export KUPE_API_KEY=kupe_...
#
#   4. WireGuard tunnel up — api.dev.int.kupe.cloud is private.
#
# Run
#
#   tofu init
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

provider "kupe" {
  host   = "https://api.dev.int.kupe.cloud"
  tenant = "kupe-test"
  # api_key reads from KUPE_API_KEY env var.
}
