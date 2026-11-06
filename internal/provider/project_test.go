package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/tebibhost/terraform-provider-tebib/internal/mockapi"
)

func projectConfig(srv *mockapi.Server, description string) string {
	return mockProvider(srv) + fmt.Sprintf(`
resource "tebib_project" "staging" {
  name        = "staging"
  description = %q
  tags        = { env = "staging" }
}

data "tebib_project" "by_name" {
  name       = tebib_project.staging.name
  depends_on = [tebib_project.staging]
}

data "tebib_project" "default" {}

data "tebib_vpc" "staging" {
  project_id = tebib_project.staging.id
  default    = true
}

resource "tebib_vpc" "staging" {
  project_id = tebib_project.staging.id
  name       = "app"
  cidr       = "10.20.0.0/16"
}
`, description)
}

func TestProjectLifecycle(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if p := srv.Find("/projects", "name", "staging"); p != "" {
				return fmt.Errorf("%s still exists", p)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: projectConfig(srv, "first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_project.staging", "organization_id", mockapi.Org),
					resource.TestCheckResourceAttr("tebib_project.staging", "state", "active"),
					resource.TestCheckResourceAttrPair("data.tebib_project.by_name", "id", "tebib_project.staging", "id"),
					resource.TestCheckResourceAttr("data.tebib_project.default", "id", mockapi.Project),
					resource.TestCheckResourceAttr("data.tebib_vpc.staging", "default", "true"),
					resource.TestCheckResourceAttrPair("data.tebib_vpc.staging", "project_id", "tebib_project.staging", "id"),
					resource.TestCheckResourceAttrPair("tebib_vpc.staging", "project_id", "tebib_project.staging", "id"),
				),
			},
			{
				Config: projectConfig(srv, "second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_project.staging", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("tebib_project.staging", "description", "second"),
			},
			{ResourceName: "tebib_project.staging", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				ResourceName: "tebib_vpc.staging", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					r := s.RootModule().Resources["tebib_vpc.staging"].Primary
					return r.Attributes["project_id"] + "/" + r.ID, nil
				},
			},
		},
	})
}

func TestCatalogueDataSources(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{{
			Config: mockProvider(srv) + `
data "tebib_images" "all" {}

data "tebib_images" "ubuntu" {
  os_family = "ubuntu"
}

data "tebib_instance_types" "all" {}

data "tebib_instance_types" "big" {
  min_vcpus = 4
}

data "tebib_prices" "now" {}

data "tebib_vpc" "default" {
  default = true
}

data "tebib_subnet" "default" {
  default = true
  vpc_id  = data.tebib_vpc.default.id
}

data "tebib_security_group" "default" {
  default = true
  vpc_id  = data.tebib_vpc.default.id
}

data "tebib_vpc" "by_name" {
  name = "default"
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.tebib_images.all", "images.#", "2"),
				resource.TestCheckResourceAttr("data.tebib_images.ubuntu", "names.#", "1"),
				resource.TestCheckResourceAttr("data.tebib_images.ubuntu", "names.0", "ubuntu-24.04"),
				resource.TestCheckResourceAttr("data.tebib_images.ubuntu", "images.0.default_user", "ubuntu"),
				resource.TestCheckResourceAttr("data.tebib_instance_types.all", "instance_types.#", "3"),
				resource.TestCheckResourceAttr("data.tebib_instance_types.big", "names.0", "g1.large"),
				resource.TestCheckResourceAttr("data.tebib_prices.now", "currency", "USD"),
				resource.TestCheckResourceAttr("data.tebib_prices.now", "region", "lab-neo-1"),
				resource.TestCheckResourceAttr("data.tebib_prices.now", "monthly_usd_by_meter.vm.b1.small", "7.30"),
				resource.TestCheckResourceAttr("data.tebib_prices.now", "egress_allowances.0.gb_per_month", "100"),
				resource.TestCheckResourceAttr("data.tebib_vpc.default", "cidr", "10.0.0.0/16"),
				resource.TestCheckResourceAttr("data.tebib_subnet.default", "default", "true"),
				resource.TestCheckResourceAttrPair("data.tebib_subnet.default", "vpc_id", "data.tebib_vpc.default", "id"),
				resource.TestCheckResourceAttr("data.tebib_security_group.default", "name", "default"),
				resource.TestCheckResourceAttrPair("data.tebib_vpc.by_name", "id", "data.tebib_vpc.default", "id"),
			),
		}},
	})
}

func TestDataSourceLookupNeedsAKey(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{{
			Config:      mockProvider(srv) + `data "tebib_vpc" "x" {}`,
			ExpectError: regexpMust(`(?s)At least one.*id.*name.*default`),
		}},
	})
}

func TestProviderNeedsCredentials(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "tebib" {
  endpoint = %q
  project  = "p-test"
}
data "tebib_images" "all" {}
`, srv.URL),
				ExpectError: regexpMust(`no credentials`),
			},
			{
				Config: fmt.Sprintf(`
provider "tebib" {
  endpoint          = %q
  access_key_id     = "TBKTEST"
  secret_access_key = "wrong"
  project           = "p-test"
}
data "tebib_images" "all" {}
`, srv.URL),
				ExpectError: regexpMust(`Unauthenticated \(HTTP 401\)`),
			},
			{
				Config: fmt.Sprintf(`
provider "tebib" {
  endpoint          = %q
  access_key_id     = "AKIAEXAMPLE"
  secret_access_key = "x"
}
data "tebib_prices" "p" {}
`, srv.URL),
				ExpectError: regexpMust(`not a TebibHost access key ID`),
			},
		},
	})
}
