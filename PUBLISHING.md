# Publishing the provider

This document covers how releases of `terraform-provider-kupe` get cut,
signed, and surfaced in both the Terraform and OpenTofu registries from
this single repo.

<!-- toc -->

* [Dual-registry model](#dual-registry-model)
* [One-time setup](#one-time-setup)
* [Per-release flow](#per-release-flow)
* [Registry submissions (one-time per registry)](#registry-submissions-one-time-per-registry)
* [Verifying a release locally](#verifying-a-release-locally)
* [Rotating the signing key](#rotating-the-signing-key)
* [Troubleshooting](#troubleshooting)

<!-- Regenerate with "pre-commit run -a markdown-toc" -->

<!-- tocstop -->

## Dual-registry model

**One repo, one GitHub release, two sets of artifacts.** Both the
Terraform registry (`registry.terraform.io`) and the OpenTofu registry
(`registry.opentofu.org`) index GitHub releases — they don't host the
binaries themselves. We attach two signed bundles to every release and
each registry picks up its own.

Why two bundles instead of one: the provider binary embeds its own
*registry address* at build time via an `ldflags` `-X` substitution:

```
-X main.providerAddress=registry.terraform.io/kupecloud/kupe   # Terraform variant
-X main.providerAddress=registry.opentofu.org/kupecloud/kupe   # OpenTofu  variant
```

The runtime self-identifies as that address, and each registry validates
the embedded value matches what it expects. Same source code, same
release tag — two binaries that differ only in that one string.

This pattern is supported by both registries. There is nothing
exclusive about either — providers like `cloudflare/cloudflare` and
`vmware/vmware` ship this way today.

Mechanics:

* Two GoReleaser configs in the repo root:
  * `.goreleaser.terraform.yaml` → `dist/terraform/` + `terraform-provider-kupe_*` archives
  * `.goreleaser.opentofu.yaml`  → `dist/opentofu/`  + `terraform-provider-kupe-opentofu_*` archives
* Each config produces per-OS/arch zips, a `SHA256SUMS` file, and a
  detached GPG signature `SHA256SUMS.sig` for the checksum file.
* The publish workflow uploads both `dist/` contents plus
  `terraform-registry-manifest.json` to the same GitHub release tag.

Users add **one** provider source in their HCL — Terraform resolves it
to `registry.terraform.io/kupecloud/kupe`, OpenTofu to
`registry.opentofu.org/kupecloud/kupe`:

```hcl
terraform {
  required_providers {
    kupe = {
      source = "kupecloud/kupe"
    }
  }
}
```

## One-time setup

### 1. Generate the release signing key

Treat this like a production secret. Both registries display the public
key fingerprint to users; rotating it is intrusive (every existing user
sees a signature-verification warning on the next upgrade), so plan to
keep this key for the long haul.

```bash
gpg --full-generate-key
```

Choose:
* Key type: **RSA 4096** (broadest registry compatibility — both
  registries accept it without caveats; ed25519 works in current
  Terraform but has had edge cases in older versions).
* Expiry: **2 years or longer**, or no expiry. The Terraform registry
  caches the public key; if it expires, releases stop signing-verifying
  even though the registry side does not auto-refresh.
* Real name + email: use a role identity (`Kupe Cloud Releases
  <releases@kupecloud.com>`), not a personal one — the key outlives any
  individual maintainer.

Capture the long key ID and fingerprint:

```bash
gpg --list-secret-keys --keyid-format=long
# look for the line:
#   sec   rsa4096/<KEYID>  YYYY-MM-DD [SC]
#         <FINGERPRINT>
```

Export both halves:

```bash
gpg --armor --export <KEYID> > kupe-release-public.asc
gpg --armor --export-secret-keys <KEYID> > kupe-release-private.asc
```

Store `kupe-release-private.asc` and the passphrase in your secret
manager (OpenBao / 1Password / whatever the team uses) before the next
step. **Do not commit either file**, and after step 2 below, shred the
local private key file:

```bash
shred -u kupe-release-private.asc   # or `rm -P` on macOS
```

### 2. Configure repo Actions secrets

```bash
gh secret set GPG_PRIVATE_KEY -R kupecloud/terraform-provider-kupe < kupe-release-private.asc
gh secret set GPG_PASSPHRASE  -R kupecloud/terraform-provider-kupe
# (prompts for the passphrase)
```

Verify:

```bash
gh secret list -R kupecloud/terraform-provider-kupe
# expect: GPG_PASSPHRASE, GPG_PRIVATE_KEY
```

### 3. Enable the publish job

The publish job in `.github/workflows/main.yaml` is intentionally
commented out until secrets are in place. Uncomment it:

```yaml
publish:
  name: Publish
  needs:
    - release
  if: needs.release.outputs.new_release_published == 'true'
  uses: ./.github/workflows/publish.yaml
  permissions:
    contents: write
  with:
    version: ${{ needs.release.outputs.new_release_version }}
  secrets: inherit
```

Push that change. The next `feat:`/`fix:` commit triggers
`semantic-release` → it cuts a new tag and GitHub release → the new
`publish` job runs `goreleaser release` twice (one per config), signs
the checksum files with the imported GPG key, and uploads the bundles
to the release.

## Per-release flow

Once setup is complete, every push to `main` that contains a release-
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
            ├─ goreleaser release --config .goreleaser.terraform.yaml
            ├─ goreleaser release --config .goreleaser.opentofu.yaml
            └─ gh release upload v<version> dist/terraform/* dist/opentofu/* terraform-registry-manifest.json
```

You do nothing per release. If the publish job fails (signing error,
goreleaser regression, etc.), the GitHub release still exists but
without artifacts — fix the underlying issue and re-run the workflow,
**not** cut a new tag.

## Registry submissions (one-time per registry)

### Terraform Registry

1. Sign in to <https://registry.terraform.io> with the GitHub account
   that owns the `kupecloud` org (or a member with `admin:org`).
2. Navigate to **Publish → Provider**.
3. Select the `kupecloud/terraform-provider-kupe` repository.
4. Upload the GPG **public** key (`kupe-release-public.asc`). The
   registry stores its fingerprint and uses it to verify the
   `SHA256SUMS.sig` on every release.
5. The registry scans existing releases and ingests any signed `v*`
   tags it finds. Subsequent releases are picked up automatically.

The provider will appear at
`https://registry.terraform.io/providers/kupecloud/kupe`.

### OpenTofu Registry

The OpenTofu registry is git-backed — submission is a PR to the
`opentofu/registry` repo.

1. Fork <https://github.com/opentofu/registry>.
2. Create `providers/k/kupecloud/kupe.json`:
   ```json
   {
     "repository": "https://github.com/kupecloud/terraform-provider-kupe"
   }
   ```
   The registry derives everything else from the GitHub releases and
   the embedded GPG signature.
3. Open a PR. The registry team auto-validates the latest signed
   release before merging. On merge, OpenTofu starts serving the
   provider at `registry.opentofu.org/kupecloud/kupe` within a few
   minutes.

After both submissions, the same `kupecloud/kupe` source in HCL
resolves to the correct registry depending on whether the user is
running `terraform` or `tofu`.

## Verifying a release locally

You can run the publish flow end-to-end on your laptop before relying
on CI:

```bash
# 1. Install goreleaser (Go build, no extra deps)
go install github.com/goreleaser/goreleaser/v2@v2.15.2

# 2. Export your signing passphrase for the GPG key the configs use
export GPG_PASSPHRASE='...'

# 3. Run both configs in snapshot mode (no tag required)
goreleaser release --snapshot --clean --config .goreleaser.terraform.yaml
goreleaser release --snapshot --clean --config .goreleaser.opentofu.yaml

# 4. Inspect what would be uploaded
ls dist/terraform/ dist/opentofu/
```

Each `dist/<variant>/` should contain:

* `terraform-provider-kupe[-opentofu]_<version>_<os>_<arch>.zip` — six
  archives (darwin/linux/windows × amd64/arm64)
* `terraform-provider-kupe[-opentofu]_<version>_SHA256SUMS`
* `terraform-provider-kupe[-opentofu]_<version>_SHA256SUMS.sig` — the
  detached signature from your GPG key
* `artifacts.json`, `metadata.json`, `config.yaml` — GoReleaser
  bookkeeping, not uploaded to the release

You can verify the signature manually:

```bash
gpg --verify \
  dist/terraform/terraform-provider-kupe_*_SHA256SUMS.sig \
  dist/terraform/terraform-provider-kupe_*_SHA256SUMS
```

## Rotating the signing key

Required if the key is compromised, lost, or about to expire. Both
registries support multiple active keys per provider, so do a
zero-downtime swap:

1. Generate the new key (same procedure as the one-time setup).
2. Add it to both registries **alongside** the old one (don't delete
   the old yet).
3. Update `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` secrets on the repo.
4. Cut a release. Verify users on the latest version can install it
   without warnings.
5. Remove the old key from each registry and from your secret manager.

If the old key is compromised (not just expired), skip the parallel
period and rotate immediately — accept that users still on the
compromised release will see verification warnings until they upgrade.

## Troubleshooting

* **`gh release view <tag>` shows no assets.** The publish job either
  did not run (secrets missing, job still commented out) or failed
  mid-way. Look at the workflow run logs; re-running the publish job
  is idempotent because of `gh release upload --clobber`.
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
