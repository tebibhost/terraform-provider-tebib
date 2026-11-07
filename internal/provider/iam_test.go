package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/tebibhost/terraform-provider-tebib/internal/mockapi"
)

type iamArgs struct {
	docVersion int
	keyStatus  string
	disabled   bool
}

func iamConfig(srv *mockapi.Server, a iamArgs) string {
	return mockProvider(srv) + fmt.Sprintf(`
resource "tebib_iam_service_account" "ci" {
  name     = "ci"
  disabled = %t
  tags     = { team = "infra" }
}

resource "tebib_iam_access_key" "ci" {
  service_account_id = tebib_iam_service_account.ci.id
  description        = "github actions"
  expires_at         = "2027-01-01T00:00:00Z"
  allowed_cidrs      = ["10.0.0.0/8", "192.0.2.10"]
  status             = %q
}

resource "tebib_iam_access_key" "forever" {
  service_account_id = tebib_iam_service_account.ci.id
  never_expires      = true
}

resource "tebib_iam_role" "deploy" {
  name = "deploy"
  trust_policy = jsonencode({
    Version   = "2026-10-08"
    Statement = [{ Effect = "Allow", Principal = { Tebib = tebib_iam_service_account.ci.trn }, Action = "iam:AssumeRole" }]
  })
  max_session_seconds = 7200
}

resource "tebib_iam_policy" "vms" {
  name        = "vms"
  description = "manage VMs"
  document = jsonencode({
    Version   = "2026-10-08"
    Statement = [{ Effect = "Allow", Action = ["compute:*"], Resource = "*", Sid = "v%d" }]
  })
}

resource "tebib_iam_policy_attachment" "ci" {
  policy    = tebib_iam_policy.vms.name
  principal = tebib_iam_service_account.ci.trn
}

resource "tebib_iam_policy_attachment" "role" {
  policy     = tebib_iam_policy.vms.id
  principal  = tebib_iam_role.deploy.trn
  project_id = %q
}

resource "tebib_iam_group" "ops" {
  name        = "ops"
  description = "operators"
}

resource "tebib_iam_group_membership" "abel" {
  group_id = tebib_iam_group.ops.id
  user_id  = "u-abel"
}
`, a.disabled, a.keyStatus, a.docVersion, mockapi.Project)
}

func TestIAMLifecycle(t *testing.T) {
	srv := newMock(t)
	policyPath := func() string { return srv.Find("/orgs/o-test/policies", "name", "vms") }
	steps := []resource.TestStep{
		{
			Config: iamConfig(srv, iamArgs{1, "active", false}),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("tebib_iam_service_account.ci", "organization_id", mockapi.Org),
				resource.TestCheckResourceAttrSet("tebib_iam_service_account.ci", "trn"),
				resource.TestCheckResourceAttrSet("tebib_iam_access_key.ci", "secret"),
				resource.TestCheckResourceAttrSet("tebib_iam_access_key.ci", "token"),
				// The API answers +00:00; the configured spelling is kept.
				resource.TestCheckResourceAttr("tebib_iam_access_key.ci", "expires_at", "2027-01-01T00:00:00Z"),
				resource.TestCheckResourceAttr("tebib_iam_access_key.ci", "allowed_cidrs.#", "2"),
				resource.TestCheckResourceAttr("tebib_iam_access_key.forever", "never_expires", "true"),
				resource.TestCheckNoResourceAttr("tebib_iam_access_key.forever", "expires_at"),
				resource.TestCheckResourceAttrSet("tebib_iam_access_key.forever", "secret"),
				resource.TestCheckResourceAttr("tebib_iam_role.deploy", "max_session_seconds", "7200"),
				resource.TestCheckResourceAttr("tebib_iam_policy.vms", "default_version", "1"),
				resource.TestCheckResourceAttr("tebib_iam_policy_attachment.ci", "principal_type", "service-account"),
				resource.TestCheckResourceAttr("tebib_iam_policy_attachment.ci", "policy", "vms"),
				resource.TestCheckResourceAttrPair("tebib_iam_policy_attachment.ci", "policy_id", "tebib_iam_policy.vms", "id"),
				resource.TestCheckResourceAttr("tebib_iam_policy_attachment.role", "project_id", mockapi.Project),
				resource.TestCheckResourceAttr("tebib_iam_group_membership.abel", "email", "u-abel@example.com"),
			),
		},
		{
			// Turn the key off and the account off: in place, and the secret is kept.
			Config: iamConfig(srv, iamArgs{1, "inactive", true}),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("tebib_iam_access_key.ci", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("tebib_iam_service_account.ci", plancheck.ResourceActionUpdate),
			}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("tebib_iam_access_key.ci", "status", "inactive"),
				resource.TestCheckResourceAttrSet("tebib_iam_access_key.ci", "secret"),
				resource.TestCheckResourceAttr("tebib_iam_service_account.ci", "disabled", "true"),
			),
		},
	}
	// Six document changes: the policy keeps at most five versions, so the provider deletes
	// the oldest non-default one before adding the sixth.
	for v := 2; v <= 6; v++ {
		want := fmt.Sprint(v)
		steps = append(steps, resource.TestStep{
			Config: iamConfig(srv, iamArgs{v, "inactive", true}),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("tebib_iam_policy.vms", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("tebib_iam_policy_attachment.ci", plancheck.ResourceActionNoop),
			}},
			Check: resource.TestCheckResourceAttr("tebib_iam_policy.vms", "default_version", want),
		})
	}
	steps = append(steps,
		resource.TestStep{
			Config: iamConfig(srv, iamArgs{6, "inactive", true}),
			Check: func(*terraform.State) error {
				if n := len(srv.Object(policyPath())["versions"].([]any)); n != 5 {
					return fmt.Errorf("policy keeps %d versions, want 5", n)
				}
				return nil
			},
		},
		resource.TestStep{ResourceName: "tebib_iam_service_account.ci", ImportState: true, ImportStateVerify: true},
		resource.TestStep{ResourceName: "tebib_iam_role.deploy", ImportState: true, ImportStateVerify: true},
		resource.TestStep{
			ResourceName: "tebib_iam_policy.vms", ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: func(s *terraform.State) (string, error) {
				return mockapi.Org + "/" + s.RootModule().Resources["tebib_iam_policy.vms"].Primary.ID, nil
			},
		},
		resource.TestStep{
			ResourceName: "tebib_iam_policy_attachment.ci", ImportState: true, ImportStateVerify: true,
			ImportStateVerifyIgnore: []string{"policy"}, // imported as the policy ID
		},
		resource.TestStep{ResourceName: "tebib_iam_group.ops", ImportState: true, ImportStateVerify: true},
		resource.TestStep{ResourceName: "tebib_iam_group_membership.abel", ImportState: true, ImportStateVerify: true},
		resource.TestStep{
			ResourceName: "tebib_iam_access_key.ci", ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: func(s *terraform.State) (string, error) {
				r := s.RootModule().Resources["tebib_iam_access_key.ci"].Primary
				return "service-account/" + r.Attributes["service_account_id"] + "/" + r.ID, nil
			},
			// The secret is only known to the create.
			ImportStateVerifyIgnore: []string{"secret", "token", "expires_at"},
		},
		resource.TestStep{
			// Drift: the member was removed from the group in the dashboard.
			PreConfig: func() {
				srv.Mutate(srv.Find("/orgs/o-test/groups", "name", "ops"), func(o map[string]any) {
					o["members"], o["member_count"] = []any{}, 0
				})
			},
			Config: iamConfig(srv, iamArgs{6, "inactive", true}),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("tebib_iam_group_membership.abel", plancheck.ResourceActionCreate),
			}},
		},
		resource.TestStep{
			// Drift: the key was deleted outside Terraform: a new one (with a new secret) is made.
			PreConfig: func() {
				srv.Remove(srv.Find("/orgs/o-test/access-keys", "description", "github actions"))
			},
			Config: iamConfig(srv, iamArgs{6, "inactive", true}),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("tebib_iam_access_key.ci", plancheck.ResourceActionCreate),
			}},
		},
	)
	unitTest(t, resource.TestCase{
		Steps: steps,
		CheckDestroy: func(*terraform.State) error {
			for _, c := range []string{"service-accounts", "roles", "policies", "groups", "policy-attachments", "access-keys"} {
				if n := srv.Count("/orgs/o-test/" + c); n != 0 {
					return fmt.Errorf("%d %s left", n, c)
				}
			}
			return nil
		},
	})
}
