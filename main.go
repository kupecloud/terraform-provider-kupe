package main

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/kupecloud/terraform-provider-kupe/internal/provider"
)

var version = "dev"
var providerAddress = "registry.terraform.io/kupecloud/kupe"

func main() {
	opts := providerserver.ServeOpts{
		Address: providerAddress,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)
	if err != nil {
		log.Fatal(err)
	}
}
