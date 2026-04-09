# terraform-provider-kupe Makefile

BINARY_NAME = terraform-provider-kupe
VERSION ?= dev
PROVIDER_ADDRESS ?= registry.terraform.io/kupecloud/kupe
TFPLUGINDOCS_VERSION ?= v0.24.0
GO := go
GOBIN ?= $(PWD)/bin
GOCACHE ?= $(PWD)/.tmp/go-build

.PHONY: all build build-terraform build-opentofu test gosec govulncheck install local-provider tidy fmt vet tofu-validate docs-install docs-generate docs-validate docs clean help

all: build

build: ## Build the provider binary
	GOCACHE=$(GOCACHE) $(GO) build -ldflags "-X main.version=$(VERSION) -X main.providerAddress=$(PROVIDER_ADDRESS)" -o $(BINARY_NAME)

build-terraform: ## Build the provider binary for Terraform registry identity
	$(MAKE) build PROVIDER_ADDRESS=registry.terraform.io/kupecloud/kupe

build-opentofu: ## Build the provider binary for OpenTofu registry identity
	$(MAKE) build PROVIDER_ADDRESS=registry.opentofu.org/kupecloud/kupe

test: ## Run unit tests
	GOCACHE=$(GOCACHE) $(GO) test -v ./...

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

clean: ## Clean build artifacts
	rm -f $(BINARY_NAME)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
