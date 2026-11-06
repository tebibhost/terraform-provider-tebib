package provider

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestCompleteExample applies examples/complete (the one in the docs-site guide) against the
// fake API, so the published example keeps working.
func TestCompleteExample(t *testing.T) {
	data, err := os.ReadFile("../../examples/complete/main.tf")
	if err != nil {
		t.Fatal(err)
	}
	srv := newMock(t)
	hcl := string(data)
	// The test provider is registered without a registry source, and points at the fake.
	hcl = regexp.MustCompile(`(?s)terraform \{.*?\n\}\n`).ReplaceAllString(hcl, "")
	hcl = strings.Replace(hcl, `provider "tebib" {}`, mockProvider(srv), 1)
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{{
			Config: hcl,
			ConfigVariables: config.Variables{
				"ssh_public_key": config.StringVariable("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIupiqSPww/kJbS2J2DgQCT/JYGFxnt4KhONzm3i992R doc"),
				"admin_cidr":     config.StringVariable("198.51.100.0/24"),
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckOutput("private_ip", "10.0.0.10"),
				resource.TestCheckResourceAttrPair("tebib_public_ip_association.web", "address", "tebib_public_ip.web", "address"),
				resource.TestCheckResourceAttr("tebib_vm.web", "root_disk_gib", "20"),
			),
		}},
	})
}
