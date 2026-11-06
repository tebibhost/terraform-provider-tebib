package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/tebibhost/terraform-provider-tebib/internal/mockapi"
)

type computeArgs struct {
	instanceType string
	power        string
	volumeSize   int
	rootDisk     int
	env          string
}

func computeConfig(srv *mockapi.Server, a computeArgs) string {
	root := ""
	if a.rootDisk > 0 {
		root = fmt.Sprintf("root_disk_gib = %d", a.rootDisk)
	}
	return mockProvider(srv) + fmt.Sprintf(`
resource "tebib_ssh_key" "me" {
  name       = "me"
  public_key = "ssh-ed25519   AAAAC3NzaC1lZDI1NTE5AAAAITEST me@example\n"
}

resource "tebib_vm" "web" {
  name          = "web"
  image         = "ubuntu-24.04"
  instance_type = %q
  power_state   = %q
  ssh_key_ids   = [tebib_ssh_key.me.id]
  user_data     = "#cloud-config\npackages: [nginx]\n"
  tags          = { env = %q }
  %s
}

resource "tebib_volume" "data" {
  name     = "data"
  size_gib = %d
}

resource "tebib_volume_attachment" "data" {
  volume_id = tebib_volume.data.id
  vm_id     = tebib_vm.web.id
}

resource "tebib_snapshot" "before" {
  name       = "before"
  vm_id      = tebib_vm.web.id
  depends_on = [tebib_volume_attachment.data]
}
`, a.instanceType, a.power, a.env, root, a.volumeSize)
}

func TestComputeLifecycle(t *testing.T) {
	srv := newMock(t)
	vmPath := func() string { return srv.Find("/projects/p-test/vms", "name", "web") }
	unitTest(t, resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if p := vmPath(); p != "" {
				return fmt.Errorf("%s still exists", p)
			}
			for _, name := range []string{"data", "web-boot"} {
				if p := srv.Find("/projects/p-test/volumes", "name", name); p != "" {
					return fmt.Errorf("%s still exists", p)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: computeConfig(srv, computeArgs{"b1.small", "running", 20, 0, "a"}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_vm.web", "state", "active"),
					resource.TestCheckResourceAttr("tebib_vm.web", "current_power_state", "running"),
					resource.TestCheckResourceAttr("tebib_vm.web", "power_state", "running"),
					resource.TestCheckResourceAttr("tebib_vm.web", "ipv4_address", "10.0.0.10"),
					resource.TestCheckResourceAttr("tebib_vm.web", "vcpus", "2"),
					resource.TestCheckResourceAttr("tebib_vm.web", "root_disk_gib", "10"),
					resource.TestCheckResourceAttr("tebib_vm.web", "hostname", "web"),
					resource.TestCheckResourceAttr("tebib_vm.web", "security_group_ids.#", "1"),
					resource.TestCheckResourceAttrSet("tebib_vm.web", "subnet_id"),
					resource.TestCheckResourceAttrSet("tebib_vm.web", "boot_volume_id"),
					resource.TestCheckResourceAttrPair("tebib_vm.web", "ssh_key_ids.0", "tebib_ssh_key.me", "id"),
					resource.TestCheckResourceAttr("tebib_ssh_key.me", "key_type", "ssh-ed25519"),
					resource.TestCheckResourceAttr("tebib_volume.data", "state", "active"),
					resource.TestCheckResourceAttr("tebib_snapshot.before", "consistency", "application"),
					resource.TestCheckResourceAttr("tebib_snapshot.before", "volume_ids.#", "2"),
					func(*terraform.State) error {
						vm := srv.Object(vmPath())
						if vm["user_data_seen"] != "#cloud-config\npackages: [nginx]\n" {
							return fmt.Errorf("user_data not sent: %v", vm["user_data_seen"])
						}
						return nil
					},
				),
			},
			{
				// Resize (stop, resize, start), grow both disks, change tags: all in place.
				Config: computeConfig(srv, computeArgs{"g1.large", "running", 30, 20, "b"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_vm.web", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("tebib_volume.data", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("tebib_volume_attachment.data", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_vm.web", "vcpus", "4"),
					resource.TestCheckResourceAttr("tebib_vm.web", "memory_mib", "8192"),
					resource.TestCheckResourceAttr("tebib_vm.web", "current_power_state", "running"),
					resource.TestCheckResourceAttr("tebib_vm.web", "root_disk_gib", "20"),
					resource.TestCheckResourceAttr("tebib_vm.web", "tags.env", "b"),
					resource.TestCheckResourceAttr("tebib_volume.data", "size_gib", "30"),
				),
			},
			{
				Config: computeConfig(srv, computeArgs{"g1.large", "stopped", 30, 20, "b"}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_vm.web", "power_state", "stopped"),
					resource.TestCheckResourceAttr("tebib_vm.web", "current_power_state", "stopped"),
				),
			},
			{
				// A smaller data volume can't shrink in place: it is replaced.
				Config: computeConfig(srv, computeArgs{"g1.large", "stopped", 25, 20, "b"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_volume.data", plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction("tebib_volume_attachment.data", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("tebib_volume.data", "size_gib", "25"),
			},
			{
				ResourceName: "tebib_vm.web", ImportState: true, ImportStateVerify: true,
				// user_data is write-only in the API.
				ImportStateVerifyIgnore: []string{"timeouts", "user_data", "status_message", "restart_required"},
			},
			{ResourceName: "tebib_volume.data", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "vm_id", "status_message"}},
			{ResourceName: "tebib_snapshot.before", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				ResourceName: "tebib_ssh_key.me", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "public_key"},
			},
			{
				ResourceName: "tebib_volume_attachment.data", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// Drift: the VM was started from the dashboard. Terraform stops it again.
				PreConfig: func() {
					srv.Mutate(vmPath(), func(o map[string]any) {
						o["desired_power_state"], o["power_state"] = "running", "running"
					})
				},
				Config: computeConfig(srv, computeArgs{"g1.large", "stopped", 25, 20, "b"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_vm.web", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("tebib_vm.web", "current_power_state", "stopped"),
			},
			{
				// Drift: the data volume was detached outside Terraform. It is attached again.
				PreConfig: func() {
					srv.Mutate(srv.Find("/projects/p-test/volumes", "name", "data"), func(o map[string]any) { o["vm_id"] = nil })
				},
				Config: computeConfig(srv, computeArgs{"g1.large", "stopped", 25, 20, "b"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_volume_attachment.data", plancheck.ResourceActionCreate),
				}},
			},
			{
				// Drift: the VM was deleted outside Terraform. It is made again.
				PreConfig: func() { srv.Remove(vmPath()) },
				Config:    computeConfig(srv, computeArgs{"g1.large", "stopped", 25, 20, "b"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_vm.web", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
	assertCalled(t, srv,
		"POST /api/v1/projects/p-test/vms/vm-", // stop/start/resize
		"POST /api/v1/projects/p-test/volumes/vol-",
		"DELETE /api/v1/projects/p-test/vms/",
	)
}

func TestVMCreateErrorTaintsAndReplaces(t *testing.T) {
	srv := newMock(t)
	cfg := mockProvider(srv) + `
resource "tebib_vm" "web" {
  name          = "web"
  image         = "ubuntu-24.04"
  instance_type = "b1.small"
}
`
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { srv.FailNextCreate("vms", "no capacity for b1.small") },
				Config:      cfg,
				ExpectError: regexpMust(`no capacity for b1.small`),
			},
			{
				// The failed VM is tainted: the next apply replaces it.
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_vm.web", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("tebib_vm.web", "state", "active"),
			},
		},
	})
}

func TestPublicIPOnVM(t *testing.T) {
	srv := newMock(t)
	cfg := mockProvider(srv) + `
data "tebib_subnet" "default" {
  default = true
}

resource "tebib_public_ip" "nat" {
  name = "nat"
}

resource "tebib_nat_gateway" "main" {
  name      = "main"
  subnet_id = data.tebib_subnet.default.id
}

resource "tebib_public_ip_association" "nat" {
  public_ip_id   = tebib_public_ip.nat.id
  nat_gateway_id = tebib_nat_gateway.main.id
}

resource "tebib_vm" "web" {
  name          = "web"
  image         = "debian-13"
  instance_type = "b1.small"
}

resource "tebib_public_ip" "web" {
  name = "web"
}

resource "tebib_public_ip_association" "web" {
  public_ip_id = tebib_public_ip.web.id
  vm_id        = tebib_vm.web.id
  depends_on   = [tebib_nat_gateway.main]
}
`
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("tebib_public_ip_association.web", "address", "tebib_public_ip.web", "address"),
					resource.TestCheckResourceAttrPair("tebib_public_ip_association.nat", "address", "tebib_public_ip.nat", "address"),
					func(*terraform.State) error {
						ip := srv.Object(srv.Find("/projects/p-test/public-ips", "name", "web"))
						if ip["association"] != "vm" {
							return fmt.Errorf("association %v", ip["association"])
						}
						return nil
					},
				),
			},
			{
				// The refresh after the association shows it on the public IP too.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_public_ip.web", "association", "vm"),
					resource.TestCheckResourceAttrPair("tebib_public_ip.web", "vm_id", "tebib_vm.web", "id"),
					resource.TestCheckResourceAttrPair("tebib_nat_gateway.main", "public_ip_id", "tebib_public_ip.nat", "id"),
				),
			},
			{ResourceName: "tebib_public_ip_association.web", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				// Drift: the address was moved off the VM. The association is made again.
				PreConfig: func() {
					srv.Mutate(srv.Find("/projects/p-test/public-ips", "name", "web"), func(o map[string]any) {
						o["association"], o["vm_id"], o["nat_gateway_id"] = "none", nil, nil
					})
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_public_ip_association.web", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
	assertCalled(t, srv, "POST /api/v1/projects/p-test/public-ips/eip-", "POST /api/v1/projects/p-test/public-ips/eip-")
}
