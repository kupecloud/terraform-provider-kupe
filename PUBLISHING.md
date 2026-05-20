# Publishing the provider

This document covers how releases of `terraform-provider-kupe` get cut,
signed, and surfaced in both the Terraform and OpenTofu registries.

The provider is already registered with both registries — this
document focuses on the **ongoing publishing flow** and operational
tasks (signature verification, key rotation, troubleshooting). For
first-time registration of a different provider, see each registry's
own docs: the [Terraform Registry publishing
guide](https://developer.hashicorp.com/terraform/registry/providers/publishing)
and the [OpenTofu Registry README](https://github.com/opentofu/registry#adding-providers-modules-or-gpg-keys-to-the-opentofu-registry).

<!-- toc -->

* [Dual-registry model](#dual-registry-model)
* [Per-release flow](#per-release-flow)
* [Verifying a release locally](#verifying-a-release-locally)
* [Rotating the signing key](#rotating-the-signing-key)
* [Troubleshooting](#troubleshooting)

<!-- Regenerate with "pre-commit run -a markdown-toc" -->

<!-- tocstop -->

## Dual-registry model

**One repo, one GitHub release, one set of artifacts — served to both
registries.** Both the Terraform Registry (`registry.terraform.io`)
and the OpenTofu Registry (`registry.opentofu.org`) index GitHub
releases; they don't host binaries themselves. Both expect the
identical asset-naming convention
(`terraform-provider-{name}_{version}_{os}_{arch}.zip` plus a
`SHA256SUMS` file and detached `.sig`), so a single signed bundle is
all either registry needs.

The provider binary embeds its registry address via an `ldflags` `-X`
substitution:

```
-X main.providerAddress=registry.terraform.io/kupecloud/kupe
```

That string is *documentation only* at runtime — it does **not** gate
which registry can serve the binary. Empirical confirmation: OpenTofu
pulls the `terraform-provider-kupe_*` zips (verified against the
lockfile `zh:` hashes), the embedded `registry.terraform.io` address
is ignored, and `tofu init` runs against `registry.opentofu.org`
without complaint. The OpenTofu Registry indexer
([source](https://github.com/opentofu/registry-stable/blob/main/src/internal/provider/version.go))
hardcodes the `terraform-provider-{name}_*` pattern with no
`-opentofu` suffix variant — there's nothing for a separate OpenTofu
build to plug into.

Mechanics:

* One GoReleaser config: [`.goreleaser.yaml`](.goreleaser.yaml)
  produces `dist/terraform-provider-kupe_{version}_{os}_{arch}.zip` × 6
  (darwin/linux/windows × amd64/arm64), a `SHA256SUMS` file, and a
  detached GPG signature `SHA256SUMS.sig`.
* The publish workflow uploads the zips + `SHA256SUMS` + `.sig` + the
  `terraform-registry-manifest.json` metadata file to the GitHub
  release tag — both registries scan that release.

Users add **one** provider source in their HCL — the short form is
registry-agnostic and each tool resolves it to its own default
registry:

```hcl
terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}
```

| Tool        | Resolves to                                |
|-------------|--------------------------------------------|
| `terraform` | `registry.terraform.io/kupecloud/kupe`     |
| `tofu`      | `registry.opentofu.org/kupecloud/kupe`     |

The same HCL config works for both tools and each user hits a fast
registry that serves their CLI. Users **can** pin to a specific
registry by writing the explicit form
(`source = "registry.terraform.io/kupecloud/kupe"`), but they rarely
need to.

## Per-release flow

The provider is already registered with both registries (signing key
uploaded, repositories indexed). The `GPG_PRIVATE_KEY` and
`GPG_PASSPHRASE` secrets are configured on the repo, and the publish
job is wired into `.github/workflows/main.yaml`. Releases happen
automatically — every push to `main` that contains a release-
worthy commit (per Conventional Commits + `semantic-release` rules)
flows as follows:

```
push → main
  └─ Main workflow
       ├─ go-lint, action-lint
       ├─ unit-tests
       ├─ gosec
       ├─ build (lint provider + validate examples + generate docs)
       ├─ release            (semantic-release: cut vX.Y.Z + GitHub release notes)
       └─ publish            (only if new_release_published == 'true')
            ├─ Import GPG key
            ├─ goreleaser release --skip=publish      (build + sign one bundle)
            └─ gh release upload v<version> dist/*.zip dist/*_SHA256SUMS{,.sig} terraform-registry-manifest.json
```

You do nothing per release. If the publish job fails (signing error,
goreleaser regression, etc.), the GitHub release still exists but
without artifacts — fix the underlying issue and re-run the workflow,
**not** cut a new tag.

## Verifying a release locally

You can run the publish flow end-to-end on your laptop before relying
on CI:

```bash
# 1. Install goreleaser (Go build, no extra deps)
go install github.com/goreleaser/goreleaser/v2@v2.15.2

# 2. Export your signing passphrase for the GPG key
export GPG_PASSPHRASE='...'

# 3. Run goreleaser in snapshot mode (no tag required)
make publish-dryrun
```

`dist/` should contain:

* `terraform-provider-kupe_<version>_<os>_<arch>.zip` — six archives
  (darwin/linux/windows × amd64/arm64)
* `terraform-provider-kupe_<version>_SHA256SUMS`
* `terraform-provider-kupe_<version>_SHA256SUMS.sig` — the detached
  signature from your GPG key
* `artifacts.json`, `metadata.json`, `config.yaml` — GoReleaser
  bookkeeping, not uploaded to the release

You can verify the signature manually:

```bash
gpg --verify \
  dist/terraform-provider-kupe_*_SHA256SUMS.sig \
  dist/terraform-provider-kupe_*_SHA256SUMS
```

## Rotating the signing key

Required if the key is compromised, lost, or about to expire. Both
registries support multiple active keys per provider, so do a
zero-downtime swap:

1. Generate the new key with `gpg --full-generate-key` (RSA 4096, the
   `Kupe Cloud Releases <releases@kupe.cloud>` user identity preserved
   so the public-facing name stays consistent across rotations).
2. Add the new public key to **both** registries alongside the old
   one — don't delete the old yet:
   * **Terraform Registry** — sign in at <https://registry.terraform.io>,
     User Settings → Signing Keys → add new key under the `kupecloud`
     namespace.
   * **OpenTofu Registry** — submit a new
     [Provider Key issue](https://github.com/opentofu/registry/issues/new?template=provider_key.yml)
     against the `kupecloud` namespace (org membership must be public
     at submission time).
3. Update `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` secrets on this repo:
   ```bash
   gh secret set GPG_PRIVATE_KEY -R kupecloud/terraform-provider-kupe < new-private.asc
   gh secret set GPG_PASSPHRASE  -R kupecloud/terraform-provider-kupe
   ```
4. Cut a release (push any `fix:` commit). Verify users on the latest
   version can install it without warnings — try `tofu init` and
   `terraform init` against a scratch config.
5. Remove the old key from each registry's settings UI, and from your
   secret manager.

If the old key is compromised (not just expired), skip the parallel
period and rotate immediately — accept that users still on the
compromised release will see verification warnings until they upgrade.

## Troubleshooting

* **`gh release view <tag>` shows no assets.** The publish job either
  did not run (secrets missing, or the `if:` gate evaluated to false
  for that commit) or failed mid-way. Look at the workflow run logs;
  re-running the publish job is idempotent because of `gh release
  upload --clobber`.
* **Goreleaser fails with `gpg: signing failed: No such file or
  directory`.** Either `GPG_PRIVATE_KEY` is malformed (must be ASCII-
  armored, including the `-----BEGIN PGP PRIVATE KEY BLOCK-----`
  header) or `GPG_PASSPHRASE` doesn't match the key. Re-export the
  key with `gpg --armor --export-secret-keys <KEYID>` and re-set the
  secret.
* **Terraform/OpenTofu registry rejects the release.** Usually means
  the SHA256SUMS file doesn't match the archives, or the signature
  doesn't verify against the public key on file. Run the local
  verification steps to reproduce, then check that the right key was
  used to sign.
