---
title: "Testing"
description: "Two-layer test strategy for the kupe terraform provider — mock-backed acceptance tests for fast CI coverage, plus manual smoke against the live dev API for release validation."
owner: platform-team
lastReviewed: "2026-04-30"
---

## Overview

The provider has two test layers — one runs every PR, one runs once before each release.

| Layer | Lives in | Backed by | Speed | What it catches |
|---|---|---|---|---|
| Mock-backed acceptance | `internal/provider/*_test.go` | In-process stateful mock kupe API ([testutil_test.go](../internal/provider/testutil_test.go)) | ~seconds | Provider code: HCL→API translation, plan diffs, drift detection, ETag handling, schema, validators, import roundtrip |
| Manual smoke | `examples/manual/` | Real deployed dev API + kupe-test tenant | ~10-15 min, human-driven | Mock drift from live contract, real auth/ratelimit, operator reconcile, the human-eye "this UX is weird" check |

```
                   confidence ↑
                              │  Manual smoke (real cluster)
                              │  Mock-backed acceptance (fast, deterministic)
                              │  Unit tests on helpers (provider_test.go, normalizeHost etc)
                              └──→ speed
```

There is no live-acceptance-test layer (the "automated `terraform apply` against real dev API" pattern). Rationale at the end of this doc.

## Layer 1 — mock-backed acceptance tests

Every resource has a `*_test.go` next to it that exercises full plan+apply+update+destroy lifecycle using `terraform-plugin-testing`'s `resource.UnitTest()` against an in-process stateful mock API. Fast, deterministic, runs on every PR.

### Run

```bash
make test                           # all tests
go test ./internal/provider/...     # only provider tests
go test -run TestAccCluster ./internal/provider/...   # narrow to one
```

### Coverage today

| Resource | Test file | Lifecycle | Import | Notes |
|---|---|---|---|---|
| `kupe_cluster` | [resource_cluster_test.go](../internal/provider/resource_cluster_test.go) | create, update version | ✓ verify | display_name + resources updates not yet covered |
| `kupe_secret` | [resource_secret_test.go](../internal/provider/resource_secret_test.go) | create, update sync target | ✓ verify | multi-target lists + reordering not covered |
| `kupe_api_key` | [resource_apikey_test.go](../internal/provider/resource_apikey_test.go) | create (admin + expires_at) | ✓ verify (key field skipped — write-once on create) | readonly role + no-expires-at variants not covered |
| `kupe_tenant_member` | [resource_member_test.go](../internal/provider/resource_member_test.go) | create, role update | ✓ verify | — |
| `kupe_alertmanager_receiver` | [resource_alertmanager_test.go](../internal/provider/resource_alertmanager_test.go) | create, body update | ✓ (body_json verify skipped — see below) | — |
| `kupe_alertmanager_routes` | same file | initial + mutate | ✓ (routes_json verify skipped) | — |
| `kupe_alertmanager_global` | same file | initial + mutate | ✓ (body_json verify skipped) | — |
| `kupe_tenant` (data) | [datasource_tenant_test.go](../internal/provider/datasource_tenant_test.go) | read | n/a | — |
| `kupe_plan` (data) | same file | read | n/a | — |
| `kupe_cluster` (data) | [datasource_cluster_test.go](../internal/provider/datasource_cluster_test.go) | read | n/a | — |

> **JSON ImportStateVerify caveat.** The alertmanager resources expose a JSON document via `body_json` / `routes_json`. The provider's [`JSONStringType`](../internal/provider/json_normalizer.go) handles semantic equality at *plan* time, so `{"a":1,"b":2}` and `{"b":2,"a":1}` don't show drift. But `terraform-plugin-testing`'s `ImportStateVerify` byte-compares state attributes, not semantic JSON equality, so a roundtrip-with-alphabetised-keys would fail. The tests pass `ImportStateVerifyIgnore: []string{"body_json"}` (or `routes_json`) to skip that field; the import handler itself is still exercised, just not byte-asserted on the JSON column.

The provider also has helper unit tests in [provider_test.go](../internal/provider/provider_test.go) for `normalizeHost`, `selectAuthToken`, and `stringValueOrEnv`. Those are pure-Go unit tests, not acceptance tests.

### What the mock covers

The mock at [testutil_test.go](../internal/provider/testutil_test.go) is a stateful in-memory implementation of the kupe-api routing tree for the `acme` tenant — full CRUD on clusters, secrets, members, apikeys, and alertmanager (receivers, routes, global). It does **not** run the kupe-api validator, so tests that need real validation (e.g. invalid alertmanager JSON) won't catch issues — those land in the manual smoke layer instead.

The mock matches the kupe-api response shapes from when it was last synchronised. Drift is the main reason Layer 2 (manual smoke) exists.

### Adding a test

Mirror the pattern of an existing `*_test.go`. The two patterns are:

- **Resource lifecycle**: `resource.TestCase` with multiple `Steps`, each a `Config` + `Check`. Add an `ImportState`/`ImportStateVerify` step at the end to roundtrip the import handler. See [resource_cluster_test.go](../internal/provider/resource_cluster_test.go) as the reference.
- **Data source**: single-step `TestCase` with a `Config` that defines a `data` block + `Check`s on its attributes. See [datasource_tenant_test.go](../internal/provider/datasource_tenant_test.go).

Test resource names should be `<resource>.test` (e.g. `kupe_cluster.test`) to match the existing convention.

## Layer 2 — manual smoke against dev

`examples/manual/` is a single tofu workspace with one HCL file per resource and per data source, all sharing a `provider.tf`. Apply against your kupe-test tenant on dev. Watch real resources spin up. Destroy. Done.

Layout:

```
examples/manual/
├── provider.tf                   # provider config — host, tenant, KUPE_API_KEY env
├── variables.tf                  # placeholder sensitive vars
├── kupe_cluster.tf               # resource + data-source smoke
├── kupe_secret.tf                # resource
├── kupe_api_key.tf               # resource
├── kupe_tenant_member.tf         # resource
├── kupe_alertmanager_receiver.tf # resource
├── kupe_alertmanager_routes.tf   # resource (depends_on receiver)
├── kupe_alertmanager_global.tf   # resource
├── kupe_tenant.tf                # data source
└── kupe_plan.tf                  # data source
```

Single workspace, single state — one `tofu apply` exercises every public surface of the provider against real dev infrastructure.

### When to run

- **Mandatory before tagging a new provider release.** Catches mock drift, broken validation, and any subtle integration with operator behaviour the mock can't simulate.
- **After non-trivial changes** to a resource's CRUD path or schema.
- **Whenever something feels off** — the human eye catches usability problems automated tests don't.

### Prerequisites

1. WireGuard tunnel up (the dev API at `api.dev.int.kupe.cloud` is private).
2. The kupe-test tenant exists on the target cluster — apply the fixture from [kupe-tests](https://github.com/kupecloud/kupe-tests):
   ```bash
   kubectl --context=<env> apply -f \
     https://raw.githubusercontent.com/kupecloud/kupe-tests/main/fixtures/tenants/kupe-test.yaml
   ```
3. Admin API key for kupe-test, exported as `KUPE_API_KEY` (the env var the provider's `api_key` field reads).
4. Provider built and installed locally:
   ```bash
   make local-provider
   ```
   This drops a dev override config under `.tmp/` so `tofu init` finds your local build.

### Run

```bash
cd examples/manual

# Smoke everything in one go (the canonical run before a release)
tofu init
tofu apply -auto-approve

# Or narrow to one resource — the `.smoke` label is consistent across every file
tofu apply -target=kupe_cluster.smoke -auto-approve

# When done
tofu destroy -auto-approve
```

### What to verify visually

- The resource appears in `kubectl get <crd> -n tenant-kupe-test`.
- For `kupe_cluster`: the corresponding `ManagedCluster` CR exists, the operator reconciles to `status.phase: Ready` within ~5-8 min, the kubeconfig endpoint is populated.
- For `kupe_alertmanager_*`: the change shows up in Mimir's alertmanager config (`kubectl logs -n mimir mimir-alertmanager-0 | grep <change>`).
- `tofu destroy` completes cleanly and the resources are actually gone (`kubectl get` returns empty).

## Why no live acceptance test layer (yet)

A natural third layer would be `*_live_test.go` — automated `TF_ACC=1` acceptance tests pointing at the real dev API. We deliberately don't have these because:

- The mock-backed Layer 1 already covers provider-side correctness (plan diff, schema, ETag, validators, drift, import).
- The kupe-api repo has its own [live test suite](https://github.com/kupecloud/kupe-api/blob/main/docs/testing.md) (`make live`) that proves the API contract end-to-end. If those pass, the contract is real.
- A 10-minute manual smoke from `examples/manual/` before each provider release catches mock drift against current reality — at a fraction of the engineering cost of a third automated layer.

Adopt live acceptance tests later if one of these triggers:

- A regression ships that one would have caught (lock the scenario in).
- Provider gets published to the public registry and CI needs to gate releases on a real-env signal.
- The mock starts drifting noticeably from kupe-api and synchronising it manually becomes a recurring chore.

## Coverage gaps still to address

Filled (see "Coverage today"):
- ✓ `kupe_cluster` data source test added.
- ✓ ImportState verification on all 7 resources.

Remaining, in priority order:

1. **Validation test paths** — the schemas use `stringvalidator.OneOf` for `role` (admin/readonly) and `type` (shared/dedicated). No test exercises a rejection. Add minimal `Config` + `ExpectError` cases.
2. **Resource-update field coverage** —
   - `kupe_cluster`: update of `display_name` and the `resources` block (cpu/memory/storage) isn't tested; only `version` is.
   - `kupe_secret`: only single-target sync is tested; multi-target lists and reordering aren't covered.
   - `kupe_api_key`: only the `admin` + `expires_at` variant is tested; `readonly` role and no-expiry are common config shapes worth covering.
3. **JSON normalisation parity at import** (provider bug, not test gap) — the alertmanager resources currently rely on `ImportStateVerifyIgnore` to skip the JSON column because the read path normalises keys differently from the write path. Long-term fix is to make read+write produce identical byte forms (or implement framework-level semantic equality for `ImportStateVerify`). Until then, the ignore is documented inline in [resource_alertmanager_test.go](../internal/provider/resource_alertmanager_test.go).

## Related

- [kupe-api testing guide](https://github.com/kupecloud/kupe-api/blob/main/docs/testing.md) — three-layer pattern (unit, kind-backed e2e, live).
- [kupe-tests](https://github.com/kupecloud/kupe-tests) — shared fixtures (the kupe-test tenant lives there) and k6 load tests.
