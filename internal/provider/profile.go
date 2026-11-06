package provider

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// The provider reads the same settings as the tebib CLI (platform/cli/src/tebib_cli/config.py):
//
//	explicit provider arguments > environment variables > CLI profile files > defaults
//
// Files live in $TEBIB_CONFIG_DIR, else $XDG_CONFIG_HOME/tebib, else ~/.config/tebib:
// config.toml (profiles) and credentials.toml (mode 0600; refused if others can read it).

const defaultProfile = "default"

// settings is the resolved provider configuration.
type settings struct {
	Profile         string
	Endpoint        string
	Organization    string
	Project         string
	AccessKeyID     string
	SecretAccessKey string
}

// explicit holds the provider block's arguments ("" = not set).
type explicit struct {
	Profile, Endpoint, Organization, Project, AccessKeyID, SecretAccessKey string
}

type cliConfig struct {
	DefaultProfile string                `toml:"default_profile"`
	Profiles       map[string]cliProfile `toml:"profiles"`
}

type cliProfile struct {
	Endpoint     string `toml:"endpoint"`
	Organization string `toml:"organization"`
	Project      string `toml:"project"`
	Credentials  string `toml:"credentials"`
}

type cliCredentials struct {
	AccessKeyID     string `toml:"access_key_id"`
	SecretAccessKey string `toml:"secret_access_key"`
}

func configDir() string {
	if d := os.Getenv("TEBIB_CONFIG_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "tebib")
}

func readTOML(path string, into any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := toml.Decode(string(data), into); err != nil {
		return false, fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	return true, nil
}

func readCredentials(path string) (map[string]cliCredentials, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %o); run: chmod 600 %s",
			path, info.Mode().Perm(), path)
	}
	creds := map[string]cliCredentials{}
	if _, err := readTOML(path, &creds); err != nil {
		return nil, err
	}
	return creds, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveSettings applies the precedence rules above.
func resolveSettings(in explicit) (settings, error) {
	dir := configDir()
	var cfg cliConfig
	if dir != "" {
		if _, err := readTOML(filepath.Join(dir, "config.toml"), &cfg); err != nil {
			return settings{}, err
		}
	}
	name := firstNonEmpty(in.Profile, os.Getenv("TEBIB_PROFILE"), cfg.DefaultProfile, defaultProfile)
	section, found := cfg.Profiles[name]
	explicitProfile := in.Profile != "" || os.Getenv("TEBIB_PROFILE") != ""
	if explicitProfile && !found && len(cfg.Profiles) > 0 {
		known := make([]string, 0, len(cfg.Profiles))
		for k := range cfg.Profiles {
			known = append(known, k)
		}
		sort.Strings(known)
		return settings{}, fmt.Errorf("profile %q not found in %s (known: %s)",
			name, filepath.Join(dir, "config.toml"), strings.Join(known, ", "))
	}

	out := settings{Profile: name}
	switch {
	case in.AccessKeyID != "" || in.SecretAccessKey != "":
		if in.AccessKeyID == "" || in.SecretAccessKey == "" {
			return settings{}, errors.New("set both access_key_id and secret_access_key, or neither")
		}
		out.AccessKeyID, out.SecretAccessKey = in.AccessKeyID, in.SecretAccessKey
	case os.Getenv("TEBIB_ACCESS_KEY_ID") != "" || os.Getenv("TEBIB_SECRET_ACCESS_KEY") != "":
		id, secret := os.Getenv("TEBIB_ACCESS_KEY_ID"), os.Getenv("TEBIB_SECRET_ACCESS_KEY")
		if id == "" || secret == "" {
			return settings{}, errors.New("set both TEBIB_ACCESS_KEY_ID and TEBIB_SECRET_ACCESS_KEY, or neither")
		}
		out.AccessKeyID, out.SecretAccessKey = id, secret
	case dir != "":
		path := filepath.Join(dir, "credentials.toml")
		creds, err := readCredentials(path)
		if err != nil {
			return settings{}, err
		}
		ref := firstNonEmpty(section.Credentials, name)
		if stored, ok := creds[ref]; ok {
			if stored.AccessKeyID == "" || stored.SecretAccessKey == "" {
				return settings{}, fmt.Errorf("credentials %q in %s lack access_key_id or secret_access_key", ref, path)
			}
			out.AccessKeyID, out.SecretAccessKey = stored.AccessKeyID, stored.SecretAccessKey
		}
	}
	out.Endpoint = strings.TrimRight(firstNonEmpty(in.Endpoint, os.Getenv("TEBIB_ENDPOINT"), section.Endpoint, client.DefaultEndpoint), "/")
	out.Organization = firstNonEmpty(in.Organization, os.Getenv("TEBIB_ORGANIZATION"), section.Organization)
	out.Project = firstNonEmpty(in.Project, os.Getenv("TEBIB_PROJECT"), section.Project)
	return out, nil
}
