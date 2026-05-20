# terraform-provider-kupe Makefile

BINARY_NAME = terraform-provider-kupe
VERSION ?= dev
PROVIDER_ADDRESS ?= registry.terraform.io/kupecloud/kupe
TFPLUGINDOCS_VERSION ?= v0.24.0
GO := go
GOBIN ?= $(PWD)/bin
GOCACHE ?= $(PWD)/.tmp/go-build

# Acceptance tests run a real Terraform/OpenTofu CLI through
# terraform-plugin-testing. When TF_ACC_TERRAFORM_PATH is unset, the framework
# falls back to downloading Terraform via hc-install and verifying its GPG
# signature — that download breaks when HashiCorp's embedded signing key
# expires, so always point the framework at a locally installed binary.
TF_ACC_TERRAFORM_PATH ?= $(shell command -v tofu 2>/dev/null || command -v terraform 2>/dev/null)
TF_ACC_PROVIDER_HOST ?= $(if $(findstring tofu,$(TF_ACC_TERRAFORM_PATH)),registry.opentofu.org,registry.terraform.io)
TF_ACC_PROVIDER_NAMESPACE ?= kupecloud

.PHONY: all build test gosec govulncheck install local-provider tidy fmt vet tofu-validate docs-install docs-generate docs-validate docs publish-dryrun clean help

all: build

# The provider's embedded `providerAddress` is set to the Terraform Registry
# address. Both registries serve the same binary — the embedded value is
# documentation only and does not gate registry compatibility. See
# PUBLISHING.md "Dual-registry model" for the rationale.
build: ## Build the provider binary
	GOCACHE=$(GOCACHE) $(GO) build -ldflags "-X main.version=$(VERSION) -X main.providerAddress=$(PROVIDER_ADDRESS)" -o $(BINARY_NAME)

test: ## Run unit and acceptance tests (auto-detects tofu/terraform)
	@if [ -z "$(TF_ACC_TERRAFORM_PATH)" ]; then \
		echo "error: no tofu or terraform binary found in PATH"; \
		echo "install OpenTofu (https://opentofu.org/docs/intro/install/) or Terraform first"; \
		exit 1; \
	fi
	GOCACHE=$(GOCACHE) \
	TF_ACC_TERRAFORM_PATH=$(TF_ACC_TERRAFORM_PATH) \
	TF_ACC_PROVIDER_HOST=$(TF_ACC_PROVIDER_HOST) \
	TF_ACC_PROVIDER_NAMESPACE=$(TF_ACC_PROVIDER_NAMESPACE) \
	$(GO) test -v ./...

gosec: ## Run gosec against the provider codebase
	GOCACHE=$(GOCACHE) GOWORK=off $(GO) run github.com/securego/gosec/v2/cmd/gosec@v2.25.0 -exclude-generated ./...

govulncheck: ## Run govulncheck against the provider codebase
	GOCACHE=$(GOCACHE) GOWORK=off $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

install: build ## Install to local Terraform plugin directory
	mkdir -p ~/.terraform.d/plugins/registry.terraform.io/kupecloud/kupe/$(VERSION)/darwin_arm64
	cp $(BINARY_NAME) ~/.terraform.d/plugins/registry.terraform.io/kupecloud/kupe/$(VERSION)/darwin_arm64/

local-provider: ## Build a local provider binary and write a dev override config under .tmp/
	./scripts/prepare_local_provider.sh

tidy: ## Run go mod tidy
	GOCACHE=$(GOCACHE) $(GO) mod tidy

fmt: ## Format code
	GOCACHE=$(GOCACHE) $(GO) fmt ./...

vet: ## Run go vet
	GOCACHE=$(GOCACHE) $(GO) vet ./...

tofu-validate: ## Run OpenTofu validation against the local provider build
	./scripts/tofu_validate_examples.sh

docs-install: ## Install tfplugindocs locally into ./bin
	GOBIN=$(GOBIN) $(GO) install github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@$(TFPLUGINDOCS_VERSION)

docs-generate: ## Generate Terraform/OpenTofu registry docs into ./docs
	GOCACHE=$(GOCACHE) ./scripts/generate_provider_schema.sh
	GOCACHE=$(GOCACHE) PATH="$(GOBIN):$(PATH)" tfplugindocs generate --provider-name kupe --rendered-provider-name kupe --providers-schema .tmp/provider-schema.json

docs-validate: ## Validate generated registry docs
	GOCACHE=$(GOCACHE) ./scripts/generate_provider_schema.sh
	GOCACHE=$(GOCACHE) PATH="$(GOBIN):$(PATH)" tfplugindocs validate --provider-name kupe --providers-schema .tmp/provider-schema.json

docs: docs-install docs-generate docs-validate ## Install tfplugindocs, generate docs, and validate them

publish-dryrun: ## Dry-run goreleaser to inspect what publish.yaml would upload (see PUBLISHING.md)
	@command -v goreleaser >/dev/null || { echo "error: goreleaser not installed; run 'go install github.com/goreleaser/goreleaser/v2@v2.15.2'"; exit 1; }
	goreleaser release --snapshot --clean --skip=publish
	@echo
	@echo "Artifacts:"
	@ls dist/*.zip dist/*SHA256SUMS* 2>/dev/null
	@echo
	@echo "Signature requires GPG_PASSPHRASE (or an unprotected key in your GnuPG keyring)."
	@echo "Skip signing with: goreleaser release --snapshot --clean --skip=sign,publish"

clean: ## Clean build artifacts
	rm -f $(BINARY_NAME)
	rm -rf dist/

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
