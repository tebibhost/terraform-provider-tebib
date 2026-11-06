package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests run against a real TebibHost endpoint. They create real, billed resources
// and delete them at the end. Run them with:
//
//	TF_ACC=1 TEBIB_ENDPOINT=... TEBIB_ACCESS_KEY_ID=... TEBIB_SECRET_ACCESS_KEY=... \
//	TEBIB_ORGANIZATION=o-... TEBIB_PROJECT=p-... go test ./internal/provider -run TestAcc -timeout 60m
//
// Use a project made for testing: a failed run can leave resources behind (they are all
// named tf-acc-*).

func accPreCheck(t *testing.T, vars ...string) {
	t.Helper()
	for _, v := range append([]string{"TEBIB_ACCESS_KEY_ID", "TEBIB_SECRET_ACCESS_KEY"}, vars...) {
		if os.Getenv(v) == "" {
			t.Fatalf("%s must be set for acceptance tests", v)
		}
	}
}

func accTest(t *testing.T, c resource.TestCase) {
	t.Helper()
	c.ProtoV6ProviderFactories = protoV6Providers
	resource.Test(t, c)
}

func TestAccFullStack(t *testing.T) {
	name := "tf-acc-" + acctest.RandString(6)
	pubkey := os.Getenv("TEBIB_ACC_SSH_PUBLIC_KEY")
	if pubkey == "" {
		// A throwaway key; nobody holds its private half.
		pubkey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIupiqSPww/kJbS2J2DgQCT/JYGFxnt4KhONzm3i992R tf-acc"
	}
	cfg := func(itype string) string {
		return fmt.Sprintf(`
provider "tebib" {}

data "tebib_images" "ubuntu" {
  os_family = "ubuntu"
}

data "tebib_instance_types" "all" {}

resource "tebib_vpc" "main" {
  name = %[1]q
  cidr = "10.77.0.0/16"
}

resource "tebib_subnet" "main" {
  name   = %[1]q
  vpc_id = tebib_vpc.main.id
  cidr   = "10.77.1.0/24"
}

resource "tebib_security_group" "web" {
  name   = %[1]q
  vpc_id = tebib_vpc.main.id
}

resource "tebib_security_group_rule" "ssh" {
  security_group_id = tebib_security_group.web.id
  protocol          = "tcp"
  port_from         = 22
  remote_cidr       = "0.0.0.0/0"
}

resource "tebib_public_ip" "nat" {
  name = "%[1]s-nat"
}

resource "tebib_nat_gateway" "main" {
  name         = %[1]q
  subnet_id    = tebib_subnet.main.id
  public_ip_id = tebib_public_ip.nat.id
}

resource "tebib_ssh_key" "me" {
  name       = %[1]q
  public_key = %[3]q
}

resource "tebib_vm" "web" {
  name               = %[1]q
  image              = data.tebib_images.ubuntu.names[0]
  instance_type      = %[2]q
  subnet_id          = tebib_subnet.main.id
  security_group_ids = [tebib_security_group.web.id]
  ssh_key_ids        = [tebib_ssh_key.me.id]
  user_data          = "#cloud-config\npackages: [nginx]\n"
}

resource "tebib_volume" "data" {
  name     = %[1]q
  size_gib = 10
}

resource "tebib_volume_attachment" "data" {
  volume_id = tebib_volume.data.id
  vm_id     = tebib_vm.web.id
}

resource "tebib_public_ip" "web" {
  name = "%[1]s-web"
}

resource "tebib_public_ip_association" "web" {
  public_ip_id = tebib_public_ip.web.id
  vm_id        = tebib_vm.web.id
  depends_on   = [tebib_nat_gateway.main]
}
`, name, itype, pubkey)
	}
	accTest(t, resource.TestCase{
		PreCheck: func() { accPreCheck(t, "TEBIB_PROJECT") },
		Steps: []resource.TestStep{
			{
				Config: cfg("b1.small"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_vm.web", "state", "active"),
					resource.TestCheckResourceAttr("tebib_vm.web", "current_power_state", "running"),
					resource.TestCheckResourceAttrSet("tebib_vm.web", "ipv4_address"),
					resource.TestCheckResourceAttrSet("tebib_public_ip_association.web", "address"),
				),
			},
			{ResourceName: "tebib_vm.web", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "user_data", "status_message", "guest_agent", "restart_required"}},
		},
	})
}

func TestAccIAM(t *testing.T) {
	name := "tf-acc-" + acctest.RandString(6)
	accTest(t, resource.TestCase{
		PreCheck: func() { accPreCheck(t, "TEBIB_ORGANIZATION") },
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "tebib" {}

resource "tebib_iam_service_account" "ci" {
  name = %[1]q
}

resource "tebib_iam_access_key" "ci" {
  service_account_id = tebib_iam_service_account.ci.id
}

resource "tebib_iam_policy" "read" {
  name = %[1]q
  document = jsonencode({
    Version   = "2026-10-08"
    Statement = [{ Effect = "Allow", Action = ["compute:Describe*"], Resource = "*" }]
  })
}

resource "tebib_iam_policy_attachment" "ci" {
  policy    = tebib_iam_policy.read.id
  principal = tebib_iam_service_account.ci.trn
}
`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("tebib_iam_access_key.ci", "secret"),
				resource.TestCheckResourceAttr("tebib_iam_policy_attachment.ci", "principal_type", "service_account"),
			),
		}},
	})
}
