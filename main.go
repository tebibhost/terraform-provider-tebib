// Command terraform-provider-tebib is the TebibHost provider for Terraform and OpenTofu.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/tebibhost/terraform-provider-tebib/internal/provider"
)

// Generate the provider docs (docs/) from the schema and examples/.
//go:generate go tool tfplugindocs generate --provider-name tebib

// version is set by goreleaser.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/tebibhost/tebib",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
