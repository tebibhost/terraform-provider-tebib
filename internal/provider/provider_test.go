package provider

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/tebibhost/terraform-provider-tebib/internal/mockapi"
)

// The unit tests run Terraform (or OpenTofu) against the fake API in internal/mockapi, so
// they need no cluster and no credentials. They still need a terraform or tofu binary:
// TF_ACC_TERRAFORM_PATH, else terraform or tofu on PATH (else terraform-plugin-testing
// downloads Terraform).
//
// Acceptance tests (acceptance_test.go) run against a real endpoint with TF_ACC=1.

func TestMain(m *testing.M) {
	// Never read the developer's own CLI profiles or credentials.
	dir, err := os.MkdirTemp("", "tebib-config")
	if err != nil {
		panic(err)
	}
	if os.Getenv("TF_ACC") == "" {
		os.Setenv("TEBIB_CONFIG_DIR", dir)
		for _, k := range []string{"TEBIB_PROFILE", "TEBIB_ENDPOINT", "TEBIB_ORGANIZATION", "TEBIB_PROJECT",
			"TEBIB_ACCESS_KEY_ID", "TEBIB_SECRET_ACCESS_KEY"} {
			os.Unsetenv(k)
		}
	}
	os.Setenv("CHECKPOINT_DISABLE", "1")
	if os.Getenv("TF_ACC_TERRAFORM_PATH") == "" {
		if p, err := exec.LookPath("terraform"); err == nil {
			os.Setenv("TF_ACC_TERRAFORM_PATH", p)
		} else if p, err := exec.LookPath("tofu"); err == nil {
			os.Setenv("TF_ACC_TERRAFORM_PATH", p)
		}
	}
	if strings.HasSuffix(os.Getenv("TF_ACC_TERRAFORM_PATH"), "tofu") && os.Getenv("TF_ACC_PROVIDER_HOST") == "" {
		os.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var protoV6Providers = map[string]func() (tfprotov6.ProviderServer, error){
	"tebib": providerserver.NewProtocol6WithError(New("test")()),
}

// mockProvider is a provider block pointed at the fake API.
func mockProvider(srv *mockapi.Server) string {
	id, secret, _ := strings.Cut(mockapi.Token, ".")
	return fmt.Sprintf(`
provider "tebib" {
  endpoint          = %q
  access_key_id     = %q
  secret_access_key = %q
  organization      = %q
  project           = %q
  poll_interval     = "1ms"
}
`, srv.URL, id, secret, mockapi.Org, mockapi.Project)
}

func newMock(t *testing.T) *mockapi.Server {
	t.Helper()
	srv := mockapi.New()
	t.Cleanup(srv.Close)
	return srv
}

func unitTest(t *testing.T, c resource.TestCase) {
	t.Helper()
	c.ProtoV6ProviderFactories = protoV6Providers
	resource.UnitTest(t, c)
}
