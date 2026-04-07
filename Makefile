# terraform-provider-kupe Makefile

BINARY_NAME = terraform-provider-kupe
VERSION ?= dev
TFPLUGINDOCS_VERSION ?= v0.24.0
GO := go
GOBIN ?= $(PWD)/bin
GOCACHE ?= $(PWD)/.tmp/go-build

.PHONY: all build test install tidy fmt vet tofu-validate docs-install docs-generate docs-validate docs clean

all: build

build: ## Build the provider binary
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)

test: ## Run unit tests
	$(GO) test -v ./...

install: build ## Install to local Terraform plugin directory
	mkdir -p ~/.terraform.d/plugins/registry.terraform.io/kupecloud/kupe/$(VERSION)/darwin_arm64
	cp $(BINARY_NAME) ~/.terraform.d/plugins/registry.terraform.io/kupecloud/kupe/$(VERSION)/darwin_arm64/

tidy: ## Run go mod tidy
	$(GO) mod tidy

fmt: ## Format code
	$(GO) fmt ./...

vet: ## Run go vet
	$(GO) vet ./...

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
