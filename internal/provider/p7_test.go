package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/tebibhost/terraform-provider-tebib/internal/mockapi"
)

// importID builds a composite import ID from attributes of a resource in the state.
func importID(name string, attrs ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return "", fmt.Errorf("%s not in the state", name)
		}
		id := ""
		for i, a := range attrs {
			if i > 0 {
				id += "/"
			}
			id += rs.Primary.Attributes[a]
		}
		return id, nil
	}
}

// -- object storage -------------------------------------------------------------------------

func storageConfig(srv *mockapi.Server, visibility, maxSize, keyStatus string) string {
	return mockProvider(srv) + fmt.Sprintf(`
resource "tebib_iam_service_account" "app" {
  name = "app"
}

resource "tebib_bucket" "assets" {
  name          = "assets-test"
  visibility    = %q
  %s
  force_destroy = true
  tags          = { app = "web" }
}

resource "tebib_storage_key" "app" {
  name               = "app"
  service_account_id = tebib_iam_service_account.app.id
  status             = %q
}

data "tebib_storage" "this" {
  depends_on = [tebib_bucket.assets]
}
`, visibility, maxSize, keyStatus)
}

func TestStorageLifecycle(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if srv.Count("/projects/p-test/buckets") != 0 || srv.Count("/projects/p-test/storage-keys") != 0 {
				return fmt.Errorf("buckets or storage keys left")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: storageConfig(srv, "private", "", "active"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_bucket.assets", "state", "active"),
					resource.TestCheckResourceAttr("tebib_bucket.assets", "url", "https://s3.lab-neo-1.example/assets-test"),
					resource.TestCheckNoResourceAttr("tebib_bucket.assets", "max_size_gib"),
					resource.TestCheckResourceAttr("tebib_storage_key.app", "effective_status", "active"),
					resource.TestCheckResourceAttrSet("tebib_storage_key.app", "secret_access_key"),
					resource.TestCheckResourceAttr("tebib_storage_key.app", "region", "lab-neo-1"),
					resource.TestCheckResourceAttrPair("tebib_storage_key.app", "holder", "tebib_iam_service_account.app", "trn"),
					resource.TestCheckResourceAttr("data.tebib_storage.this", "region", "lab-neo-1"),
					resource.TestCheckResourceAttr("data.tebib_storage.this", "buckets", "1"),
				),
			},
			{
				// Visibility, the size cap and the key's status change in place.
				Config: storageConfig(srv, "public-read", "max_size_gib = 50", "inactive"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_bucket.assets", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("tebib_storage_key.app", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_bucket.assets", "visibility", "public-read"),
					resource.TestCheckResourceAttr("tebib_bucket.assets", "max_size_gib", "50"),
					resource.TestCheckResourceAttr("tebib_storage_key.app", "status", "inactive"),
					resource.TestCheckResourceAttrSet("tebib_storage_key.app", "secret_access_key"),
				),
			},
			{
				// Removing the cap sends 0.
				Config: storageConfig(srv, "public-read", "", "inactive"),
				Check:  resource.TestCheckNoResourceAttr("tebib_bucket.assets", "max_size_gib"),
			},
			{
				ResourceName: "tebib_bucket.assets", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "force_destroy", "status_message"},
			},
			{
				ResourceName: "tebib_storage_key.app", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "secret_access_key", "service_account_id", "status_message"},
			},
			{
				// The bucket filled up; force_destroy deletes it anyway.
				PreConfig: func() { srv.SetObjects(srv.Find("/projects/p-test/buckets", "name", "assets-test"), 3) },
				Config:    storageConfig(srv, "public-read", "", "inactive"),
				PlanOnly:  true,
			},
		},
	})
}

func TestBucketNotEmptyIsNotDeleted(t *testing.T) {
	srv := newMock(t)
	cfg := mockProvider(srv) + `
resource "tebib_bucket" "logs" {
  name = "logs-test"
}
`
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig:   func() { srv.SetObjects(srv.Find("/projects/p-test/buckets", "name", "logs-test"), 2) },
				Config:      cfg,
				Destroy:     true,
				ExpectError: regexpMust(`holds 2 object\(s\)`),
			},
			{
				// Emptied: now it goes.
				PreConfig: func() { srv.SetObjects(srv.Find("/projects/p-test/buckets", "name", "logs-test"), 0) },
				Config:    cfg,
			},
		},
	})
}

// -- load balancers -------------------------------------------------------------------------

func lbConfig(srv *mockapi.Server, tgPort int, healthPath, listenerGroup string) string {
	return mockProvider(srv) + fmt.Sprintf(`
resource "tebib_vm" "web" {
  name          = "web"
  image         = "ubuntu-24.04"
  instance_type = "b1.small"
}

resource "tebib_load_balancer" "web" {
  name = "web"
  tags = { app = "web" }
}

resource "tebib_load_balancer_target_group" "web" {
  load_balancer_id = tebib_load_balancer.web.id
  name             = "web"
  port             = %d
  health_check = {
    protocol = "http"
    path     = %q
  }
}

resource "tebib_load_balancer_target_group" "blue" {
  load_balancer_id = tebib_load_balancer.web.id
  name             = "blue"
  port             = 8080
  algorithm        = "least_connections"
}

resource "tebib_load_balancer_listener" "http" {
  load_balancer_id = tebib_load_balancer.web.id
  port             = 80
  target_group_id  = tebib_load_balancer_target_group.%s.id
}

resource "tebib_load_balancer_target" "web" {
  load_balancer_id = tebib_load_balancer.web.id
  target_group_id  = tebib_load_balancer_target_group.web.id
  vm_id            = tebib_vm.web.id
}
`, tgPort, healthPath, listenerGroup)
}

func TestLoadBalancerLifecycle(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if srv.Count("/projects/p-test/load-balancers") != 0 {
				return fmt.Errorf("load balancer left")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: lbConfig(srv, 80, "/healthz", "web"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_load_balancer.web", "state", "active"),
					resource.TestCheckResourceAttr("tebib_load_balancer.web", "scheme", "public"),
					resource.TestCheckResourceAttrSet("tebib_load_balancer.web", "public_address"),
					resource.TestCheckResourceAttr("tebib_load_balancer.web", "security_group_ids.#", "1"),
					resource.TestCheckResourceAttr("tebib_load_balancer.web", "nodes_ready", "2"),
					resource.TestCheckResourceAttr("tebib_load_balancer_target_group.web", "health_check.protocol", "http"),
					resource.TestCheckResourceAttr("tebib_load_balancer_target_group.web", "health_check.interval_seconds", "10"),
					resource.TestCheckResourceAttr("tebib_load_balancer_target_group.blue", "health_check.protocol", "tcp"),
					resource.TestCheckResourceAttr("tebib_load_balancer_listener.http", "protocol", "tcp"),
					resource.TestCheckResourceAttr("tebib_load_balancer_target.web", "effective_port", "80"),
					resource.TestCheckNoResourceAttr("tebib_load_balancer_target.web", "port"),
				),
			},
			{
				// The group's port and health check change in place; so does the listener's group.
				Config: lbConfig(srv, 8000, "/ready", "blue"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_load_balancer_target_group.web", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("tebib_load_balancer_listener.http", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("tebib_load_balancer_target.web", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_load_balancer_target_group.web", "port", "8000"),
					resource.TestCheckResourceAttr("tebib_load_balancer_target_group.web", "health_check.path", "/ready"),
					resource.TestCheckResourceAttrPair("tebib_load_balancer_listener.http", "target_group_id", "tebib_load_balancer_target_group.blue", "id"),
				),
			},
			{
				ResourceName: "tebib_load_balancer.web", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "status_message"},
			},
			{
				ResourceName: "tebib_load_balancer_target_group.web", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:       importID("tebib_load_balancer_target_group.web", "load_balancer_id", "id"),
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				ResourceName: "tebib_load_balancer_listener.http", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:       importID("tebib_load_balancer_listener.http", "load_balancer_id", "id"),
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				ResourceName: "tebib_load_balancer_target.web", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:       importID("tebib_load_balancer_target.web", "load_balancer_id", "target_group_id", "id"),
				ImportStateVerifyIgnore: []string{"timeouts", "health", "health_reason"},
			},
			{
				// Drift: the listener was deleted outside Terraform. It is made again.
				PreConfig: func() {
					srv.Mutate(srv.Find("/projects/p-test/load-balancers", "name", "web"), func(o map[string]any) {
						o["listeners"] = []any{}
					})
				},
				Config: lbConfig(srv, 8000, "/ready", "blue"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_load_balancer_listener.http", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

// -- DNS ------------------------------------------------------------------------------------

func dnsConfig(srv *mockapi.Server, mx string, ttl int) string {
	return mockProvider(srv) + fmt.Sprintf(`
resource "tebib_dns_zone" "example" {
  domain = "Example.com"
}

resource "tebib_dns_record" "www" {
  zone_id = tebib_dns_zone.example.id
  name    = "www"
  type    = "CNAME"
  values  = ["web.example.net"]
}

resource "tebib_dns_record" "mx" {
  zone_id = tebib_dns_zone.example.id
  type    = "MX"
  ttl     = %d
  values  = [%s]
}

resource "tebib_dns_record" "spf" {
  zone_id = tebib_dns_zone.example.id
  type    = "TXT"
  values  = ["v=spf1 -all"]
}
`, ttl, mx)
}

func TestDNSLifecycle(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			if srv.Count("/projects/p-test/dns/zones") != 0 {
				return fmt.Errorf("zone left")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: dnsConfig(srv, `"10 mail.example.com"`, 3600),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "domain", "Example.com"),
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "name", "example.com"),
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "status", "pending_verification"),
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "state", "creating"),
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "nameservers.#", "2"),
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "verification_txt_name", "_tebib-verify.example.com"),
					// The user's spelling is kept; the canonical form is beside it.
					resource.TestCheckResourceAttr("tebib_dns_record.www", "values.0", "web.example.net"),
					resource.TestCheckResourceAttr("tebib_dns_record.www", "canonical_values.0", "web.example.net."),
					resource.TestCheckResourceAttr("tebib_dns_record.www", "fqdn", "www.example.com."),
					resource.TestCheckResourceAttr("tebib_dns_record.mx", "name", "@"),
					resource.TestCheckResourceAttr("tebib_dns_record.spf", "canonical_values.0", `"v=spf1 -all"`),
				),
			},
			{
				// A second MX and a new TTL change the record set in place.
				Config: dnsConfig(srv, `"10 mail.example.com", "20 backup.example.com."`, 600),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_dns_record.mx", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("tebib_dns_record.www", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_dns_record.mx", "ttl", "600"),
					resource.TestCheckResourceAttr("tebib_dns_record.mx", "canonical_values.#", "2"),
					resource.TestCheckResourceAttr("tebib_dns_record.mx", "canonical_values.1", "20 backup.example.com."),
				),
			},
			{
				// The domain was delegated: the zone is served now. Nothing to change.
				PreConfig: func() { srv.VerifyZone(srv.Find("/projects/p-test/dns/zones", "domain", "example.com")) },
				Config:    dnsConfig(srv, `"10 mail.example.com", "20 backup.example.com."`, 600),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "status", "active"),
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "served", "true"),
					resource.TestCheckResourceAttr("tebib_dns_zone.example", "verification_method", "ns"),
				),
			},
			{
				ResourceName: "tebib_dns_zone.example", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "domain", "status_message"},
			},
			{
				ResourceName: "tebib_dns_record.www", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:       importID("tebib_dns_record.www", "zone_id", "id"),
				ImportStateVerifyIgnore: []string{"timeouts", "values"},
			},
			{
				// Drift: someone changed the CNAME in the dashboard. Terraform puts it back.
				PreConfig: func() {
					zone := srv.Find("/projects/p-test/dns/zones", "domain", "example.com")
					srv.Mutate(srv.Find(zone+"/records", "name", "www"), func(o map[string]any) {
						o["values"] = []any{"elsewhere.example.org."}
					})
				},
				Config: dnsConfig(srv, `"10 mail.example.com", "20 backup.example.com."`, 600),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_dns_record.www", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("tebib_dns_record.www", "canonical_values.0", "web.example.net."),
			},
		},
	})
}

// -- snapshot schedules ---------------------------------------------------------------------

func scheduleConfig(srv *mockapi.Server, timing, retention string) string {
	return mockProvider(srv) + fmt.Sprintf(`
resource "tebib_vm" "db" {
  name          = "db"
  image         = "ubuntu-24.04"
  instance_type = "b1.small"
}

resource "tebib_snapshot_schedule" "nightly" {
  name     = "nightly"
  vm_id    = tebib_vm.db.id
  timezone = "Africa/Nairobi"
  %s
  %s
}
`, timing, retention)
}

func TestSnapshotScheduleLifecycle(t *testing.T) {
	srv := newMock(t)
	unitTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				// Snapshots would pile up forever: one retention rule is required.
				Config:      scheduleConfig(srv, `frequency = "daily"`, ""),
				PlanOnly:    true,
				ExpectError: regexpMust(`(?s)At least one of these attributes must be configured`),
			},
			{
				Config: scheduleConfig(srv, `frequency = "daily"
  hour = 2
  minute = 30`, "retention_count = 7"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_snapshot_schedule.nightly", "effective_cron", "30 2 * * *"),
					resource.TestCheckResourceAttr("tebib_snapshot_schedule.nightly", "enabled", "true"),
					resource.TestCheckResourceAttr("tebib_snapshot_schedule.nightly", "weekday", "0"),
					resource.TestCheckNoResourceAttr("tebib_snapshot_schedule.nightly", "cron"),
				),
			},
			{
				// Weekly on Saturday, kept by age instead of count: in place.
				Config: scheduleConfig(srv, `frequency = "weekly"
  weekday = 6
  hour = 2
  minute = 30`, "retention_days = 30"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tebib_snapshot_schedule.nightly", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_snapshot_schedule.nightly", "effective_cron", "30 2 * * 6"),
					resource.TestCheckResourceAttr("tebib_snapshot_schedule.nightly", "retention_days", "30"),
					resource.TestCheckNoResourceAttr("tebib_snapshot_schedule.nightly", "retention_count"),
				),
			},
			{
				ResourceName: "tebib_snapshot_schedule.nightly", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "status_message"},
			},
			{
				Config: scheduleConfig(srv, `frequency = "custom"
  cron = "15 */6 * * 1-5"
  enabled = false`, "retention_days = 30"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tebib_snapshot_schedule.nightly", "effective_cron", "15 */6 * * 1-5"),
					resource.TestCheckResourceAttr("tebib_snapshot_schedule.nightly", "enabled", "false"),
					resource.TestCheckNoResourceAttr("tebib_snapshot_schedule.nightly", "next_run_at"),
				),
			},
		},
	})
}
