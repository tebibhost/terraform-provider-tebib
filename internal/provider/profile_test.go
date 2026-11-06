package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These follow platform/cli/tests/test_config.py: the provider must resolve settings the
// same way the CLI does.

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func cliDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TEBIB_CONFIG_DIR", dir)
	for _, k := range []string{"TEBIB_PROFILE", "TEBIB_ENDPOINT", "TEBIB_ORGANIZATION", "TEBIB_PROJECT",
		"TEBIB_ACCESS_KEY_ID", "TEBIB_SECRET_ACCESS_KEY"} {
		t.Setenv(k, "")
	}
	writeFile(t, filepath.Join(dir, "config.toml"), `
default_profile = "lab"

[profiles.lab]
endpoint = "https://lab.example/"
organization = "o-lab"
project = "p-lab"
credentials = "labkeys"

[profiles.prod]
endpoint = "https://prod.example"
project = "p-prod"
`, 0o644)
	writeFile(t, filepath.Join(dir, "credentials.toml"), `
[labkeys]
access_key_id = "TBKLAB"
secret_access_key = "lab-secret"

[prod]
access_key_id = "TBKPROD"
secret_access_key = "prod-secret"
`, 0o600)
	return dir
}

func TestDefaultProfileFromFiles(t *testing.T) {
	cliDir(t)
	s, err := resolveSettings(explicit{})
	if err != nil {
		t.Fatal(err)
	}
	want := settings{Profile: "lab", Endpoint: "https://lab.example", Organization: "o-lab", Project: "p-lab",
		AccessKeyID: "TBKLAB", SecretAccessKey: "lab-secret"}
	if s != want {
		t.Fatalf("got %+v\nwant %+v", s, want)
	}
}

func TestProfileFromEnvAndCredentialsByProfileName(t *testing.T) {
	cliDir(t)
	t.Setenv("TEBIB_PROFILE", "prod")
	s, err := resolveSettings(explicit{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Endpoint != "https://prod.example" || s.AccessKeyID != "TBKPROD" || s.Organization != "" {
		t.Fatalf("got %+v", s)
	}
}

func TestEnvironmentBeatsFilesAndExplicitBeatsEnvironment(t *testing.T) {
	cliDir(t)
	t.Setenv("TEBIB_ENDPOINT", "https://env.example")
	t.Setenv("TEBIB_PROJECT", "p-env")
	t.Setenv("TEBIB_ACCESS_KEY_ID", "TBKENV")
	t.Setenv("TEBIB_SECRET_ACCESS_KEY", "env-secret")
	s, err := resolveSettings(explicit{Project: "p-explicit"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Endpoint != "https://env.example" || s.Project != "p-explicit" || s.AccessKeyID != "TBKENV" || s.Organization != "o-lab" {
		t.Fatalf("got %+v", s)
	}
	s, err = resolveSettings(explicit{AccessKeyID: "TBKX", SecretAccessKey: "x"})
	if err != nil || s.AccessKeyID != "TBKX" {
		t.Fatalf("explicit keys should win: %+v %v", s, err)
	}
}

func TestHalfCredentialsAreRefused(t *testing.T) {
	cliDir(t)
	t.Setenv("TEBIB_ACCESS_KEY_ID", "TBKENV")
	if _, err := resolveSettings(explicit{}); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("want an error, got %v", err)
	}
	t.Setenv("TEBIB_ACCESS_KEY_ID", "")
	if _, err := resolveSettings(explicit{SecretAccessKey: "x"}); err == nil {
		t.Fatal("want an error")
	}
}

func TestUnknownExplicitProfile(t *testing.T) {
	cliDir(t)
	_, err := resolveSettings(explicit{Profile: "nope"})
	if err == nil || !strings.Contains(err.Error(), "known: lab, prod") {
		t.Fatalf("got %v", err)
	}
}

func TestReadableCredentialsFileIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	dir := cliDir(t)
	if err := os.Chmod(filepath.Join(dir, "credentials.toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveSettings(explicit{})
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("got %v", err)
	}
}

func TestNoFilesGivesDefaults(t *testing.T) {
	t.Setenv("TEBIB_CONFIG_DIR", t.TempDir())
	for _, k := range []string{"TEBIB_PROFILE", "TEBIB_ENDPOINT", "TEBIB_ACCESS_KEY_ID", "TEBIB_SECRET_ACCESS_KEY", "TEBIB_PROJECT", "TEBIB_ORGANIZATION"} {
		t.Setenv(k, "")
	}
	s, err := resolveSettings(explicit{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Endpoint != "https://cloud.tebibhost.com" || s.Profile != "default" || s.AccessKeyID != "" {
		t.Fatalf("got %+v", s)
	}
}
