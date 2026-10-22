---
title: "Testing"
description: "Two-layer test strategy for the kupe terraform provider — mock-backed acceptance tests for fast CI coverage, plus manual smoke against the live dev API for release validation."
owner: platform-team
lastReviewed: "2026-10-10"
---

## Overview

The provider has two test layers — one runs every PR, one runs once before each release.

| Layer | Lives in | Backed by | Speed | What it catches |
|---|---|---|---|---|
| Mock-backed acceptance | `internal/provider/*_test.go` | In-process stateful mock kupe API ([testutil_test.go](../internal/provider/testutil_test.go)) | ~seconds | Provider code: HCL→API translation, plan diffs, drift detection, ETag handling, schema, validators, import roundtrip |
| Manual smoke | `test/manual/` | Real deployed dev API + kupe-test tenant | ~10-15 min, human-driven | Mock drift from live contract, real auth/ratelimit, operator reconcile, the human-eye "this UX is weird" check |

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
| `kupe_cluster` | [resource_cluster_test.go](../internal/provider/resource_cluster_test.go) | create, update version | ✓ verify | display_name updates not yet covered; name validator (starts with a letter, no `--`, at most 63 characters); the Create/Update wait, ETag retry and delete ([resource_cluster_wait_test.go](../internal/provider/resource_cluster_wait_test.go)): resources update waits for the post-PATCH generation and through Upgrading, rides out Degraded at the new generation within the 2 minute grace (the order the platform produces when the change heals a Degraded cluster), fails on Degraded beyond the grace and at once on Terminating, a timeout while Degraded fails, a timeout before the change is seen warns, a timeout while the platform fails to apply the change fails, a Degraded create is tainted and replaced, a stale ETag is retried once when only status moved and a concurrent spec change is reported, a delete is retried on 409 and times out with the delete hint; high_availability kept when unset ([resource_database_wait_test.go](../internal/provider/resource_database_wait_test.go)) |
| `kupe_secret` | [resource_secret_test.go](../internal/provider/resource_secret_test.go) | create, update sync target | ✓ verify | multi-target lists + reordering not covered |
| `kupe_api_key` | [resource_apikey_test.go](../internal/provider/resource_apikey_test.go) | create (admin + expires_at) | ✓ verify (key field skipped — write-once on create) | readonly role + no-expires-at variants not covered |
| `kupe_tenant_member` | [resource_member_test.go](../internal/provider/resource_member_test.go) | create, role update | ✓ verify | — |
| `kupe_alertmanager_receiver` | [resource_alertmanager_test.go](../internal/provider/resource_alertmanager_test.go) | create, body update | ✓ (body_json verify skipped — see below) | — |
| `kupe_alertmanager_routes` | same file | initial + mutate | ✓ (routes_json verify skipped) | — |
| `kupe_alertmanager_global` | same file | initial + mutate | ✓ (body_json verify skipped) | — |
| `kupe_database` | [resource_database_test.go](../internal/provider/resource_database_test.go) | create, update resources, HA on and off, deletion protection off | ✓ verify | plan-time errors for engine change, storage shrink and version change; name (at most 44 characters, a 44-character name created) and quantity validators; update retried once on a stale ETag when only status changed, concurrent setting changes reported; creation paused; delete refused while protected; delete retried on 409 and the timeout message; databases disabled still allows unprotect and destroy; the Create/Update wait ([resource_database_wait_test.go](../internal/provider/resource_database_wait_test.go)): waits while the instances roll, rides out Degraded within the grace, fails on Degraded beyond it and on Terminating, a timeout while Degraded fails, a timeout while the platform fails to apply the change fails, a timeout while rolling or provisioning warns, a failed create is tainted and replaced, a deletion_protection-only change is not waited for |
| `kupe_database_binding` | [resource_database_binding_test.go](../internal/provider/resource_database_binding_test.go) | create, `namespaces` added and removed in place (the complete sorted list, with If-Match), replace on role / secret_name / name / cluster change | ✓ verify | removing name or secret_name keeps the current one, default name `{database}-{cluster}-{role}` and too long for 63 characters, role validator (`app` refused like any other value), 1 to 10 namespaces, reserved namespace, `--` in database or cluster, a database name over 44 characters still bindable; the Create/Update wait: Update waits until the status describes the new list (added and removed), a Failed namespace ends Create (tainted, replaced) and Update (recorded) with its name, a removed namespace still Failed does not fail the Update, a stale ETag is retried once when only status moved and a concurrent namespaces change is reported, an update while databases are disabled is refused |
| `kupe_registry` | [resource_registry_test.go](../internal/provider/resource_registry_test.go) | create, `storage_limit` set and cleared in place, deletion protection off (a PATCH that is not waited for) | ✓ verify | `name` defaults to `registry` and any other name, an import ID other than `registry`, a decimal or sub-Mi unit and zero are refused at plan; POST carries `deletionProtection: true`; destroy refused while protected and the registry kept in state; a second registry gets the import hint; registry switched off: refresh keeps state with an empty plan, a storage change fails with the hint, protection can be lifted and the registry destroyed, create fails with the hint; `registries_unavailable` on a read and a kupe API without registry routes fail the refresh and keep state; delete waits until gone; Terminating fails the create; the delete notice is in the docs |
| `kupe_registry_binding` | [resource_registry_binding_test.go](../internal/provider/resource_registry_binding_test.go) | create pull-only and with push namespaces, `push_namespaces` added and removed in place (one PATCH with the sorted list and If-Match, no DELETE or POST; `[]` to remove all), replace on `secret_name` and `cluster` (DELETE then POST, no PATCH) | ✓ verify (`registry/<cluster>`) | `name` equals `cluster`; 11 push namespaces, `kube-system`, `argocd`, a non-DNS label, `secret_name = "kupe-registry-pull"`, another registry and `--` in the cluster refused at plan; other import IDs refused; an already connected cluster gets the import hint with its name; a create before the registry exists gets the hint to reference `kupe_registry.this`; a Failed push namespace ends Create (tainted, replaced); registry switched off: adding a namespace fails with the hint, removing one is served, destroy works; the restart warning on Create and not on Read |
| `kupe_registry_token` | [resource_registry_token_test.go](../internal/provider/resource_registry_token_test.go) | create (POST `expiresInDays` when set, omitted when unset with 90 days recorded), refresh records the platform's username and `last_used_at` and keeps the secret, revoked outside Terraform is removed and created again, every input replaces, a `timeouts` change is in place | ✓ verify (`registry/<id>`; secret null after import) | `secret` Sensitive, computed and described verbatim in the docs; plan-time lifetime, display name (characters, not bytes; a tab or newline refused), access and registry checks; other import IDs refused; `registries_unavailable` on a read keeps state; the token limit hint; a create before the registry exists gets the hint to reference `kupe_registry.this` (and no other 404 does); a Failed token ends Create (tainted, replaced); Create waits while Pending; an expired token still present warns on refresh and stays; the secret never appears in TRACE logs |
| `kupe_database` (data) | [resource_database_test.go](../internal/provider/resource_database_test.go) | read | n/a | — |
| `kupe_tenant` (data) | [datasource_tenant_test.go](../internal/provider/datasource_tenant_test.go) | read | n/a | — |
| `kupe_plan` (data) | same file | read | n/a | `max_databases` set, `0` and null; the registry allowance set, `0` (unlimited) and null |
| `kupe_registry` (data) | [datasource_registry_test.go](../internal/provider/datasource_registry_test.go) | read with the name implicit and explicit | n/a | another name refused at plan; a tenant with no registry is an error |
| `kupe_cluster` (data) | [datasource_cluster_test.go](../internal/provider/datasource_cluster_test.go) | read | n/a | — |

> **JSON ImportStateVerify caveat.** The alertmanager resources expose a JSON document via `body_json` / `routes_json`. The provider's [`JSONStringType`](../internal/provider/json_normalizer.go) handles semantic equality at *plan* time, so `{"a":1,"b":2}` and `{"b":2,"a":1}` don't show drift. But `terraform-plugin-testing`'s `ImportStateVerify` byte-compares state attributes, not semantic JSON equality, so a roundtrip-with-alphabetised-keys would fail. The tests pass `ImportStateVerifyIgnore: []string{"body_json"}` (or `routes_json`) to skip that field; the import handler itself is still exercised, just not byte-asserted on the JSON column.

The provider also has helper unit tests in [provider_test.go](../internal/provider/provider_test.go) for `normalizeHost`, `selectAuthToken`, and `stringValueOrEnv`. Those are pure-Go unit tests, not acceptance tests.

### What the mock covers

The mock at [testutil_test.go](../internal/provider/testutil_test.go) is a stateful in-memory implementation of the kupe-api routing tree for the `acme` tenant — full CRUD on clusters, secrets, members, apikeys, alertmanager (receivers, routes, global), the registry, its cluster connections and access tokens (`testutil_registry_test.go`: what stays served while the registry is switched off, the plain 404 of a kupe API without registry routes, scripted phases), and databases and their bindings (including If-Match, the deletion-protection 409, the PATCH field allow-list, the storage-shrink and creation-paused 400s, and what stays served while databases are disabled). It does **not** run the kupe-api validator, so tests that need real validation (e.g. invalid alertmanager JSON) won't catch issues — those land in the manual smoke layer instead.

The mock matches the kupe-api response shapes from when it was last synchronised. Drift is the main reason Layer 2 (manual smoke) exists.

### Adding a test

Mirror the pattern of an existing `*_test.go`. The two patterns are:

- **Resource lifecycle**: `resource.TestCase` with multiple `Steps`, each a `Config` + `Check`. Add an `ImportState`/`ImportStateVerify` step at the end to roundtrip the import handler. See [resource_cluster_test.go](../internal/provider/resource_cluster_test.go) as the reference.
- **Data source**: single-step `TestCase` with a `Config` that defines a `data` block + `Check`s on its attributes. See [datasource_tenant_test.go](../internal/provider/datasource_tenant_test.go).

Test resource names should be `<resource>.test` (e.g. `kupe_cluster.test`) to match the existing convention.

## Layer 2 — manual smoke against dev

`test/manual/` is a single tofu workspace with one HCL file per resource and per data source, all sharing a `provider.tf`. Apply against your kupe-test tenant on dev. Watch real resources spin up. Destroy. Done.

Layout:

```
test/manual/
├── provider.tf                   # provider config — host, tenant, KUPE_API_KEY env
├── variables.tf                  # placeholder sensitive vars
├── kupe_cluster.tf               # resource + data-source smoke
├── kupe_database.tf              # database + binding + data-source smoke
├── kupe_registry.tf              # registry + connection + token + data-source smoke
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

1. Network access to your target Kupe API.
2. A test tenant.
3. An API key with sufficient permissions
4. `KUPE_HOST`, `KUPE_TENANT`, and `KUPE_API_KEY` exported in your shell. The provider reads all three from the environment, so no environment-specific URL or tenant name is committed to `test/manual/`:
   ```bash
   export KUPE_HOST=https://api.<your-env>.kupe.cloud
   export KUPE_TENANT=<your-test-tenant>
   export KUPE_API_KEY=kupe_...
   ```
5. Provider built and installed locally:
   ```bash
   make local-provider
   export TF_CLI_CONFIG_FILE="$PWD/.tmp/tfdevrc"
   ```
   This drops a dev override config under `.tmp/` so tofu loads the local binary directly. **Skip `tofu init`** when the dev override is active — it does a registry lookup that fails on `registry.opentofu.org`, and the override means init is unnecessary anyway.

### Run

```bash
cd test/manual

# Smoke everything in one go (the canonical run before a release)
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
- A 10-minute manual smoke from `test/manual/` before each provider release catches mock drift against current reality — at a fraction of the engineering cost of a third automated layer.

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
