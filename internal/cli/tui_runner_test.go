package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

func TestWithOverridesKeepsTheFlagsOverTheFile(t *testing.T) {
	build := withOverrides(config.Overrides{MCPServerPrefix: new("flag")}, func(cfg config.Config, _ *config.Organization) (string, error) {
		return cfg.MCPServerPrefix, nil
	})
	file := config.Defaults()
	file.MCPServerPrefix = "file"
	got, err := build(file, nil)
	if err != nil || got != "flag" {
		t.Fatalf("prefix = %q, err = %v; want the flag", got, err)
	}
}

func TestWithOverridesReportsAnInvalidFlag(t *testing.T) {
	build := withOverrides(config.Overrides{JumphostTag: new("nope")}, func(config.Config, *config.Organization) (string, error) {
		return "built", nil
	})
	if _, err := build(config.Defaults(), nil); err == nil || !strings.Contains(err.Error(), "--jumphost-tag") {
		t.Fatalf("err = %v, want the flag named", err)
	}
}

func TestMCPHostsSharesOneHostPerClients(t *testing.T) {
	hosts := newMCPHosts()
	auto := config.Defaults()
	none := config.Defaults()
	none.MCPClients = "none"
	//nolint:staticcheck // caching: first call creates, second call retrieves the same host.
	if hosts.For(&auto) != hosts.For(&auto) {
		t.Fatal("the same clients must share one host")
	}
	if hosts.For(&auto) == hosts.For(&none) {
		t.Fatal("other clients must get another host")
	}
}

func TestReloadingDiscoveryFollowsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"defaultDbUser": "one"}`)
	source := newRunnerSource(path, nil, func(cfg config.Config, _ *config.Organization) (*jumphost.Runner, error) {
		return jumphost.NewRunner(&jumphost.RunnerOptions{DefaultDBUser: cfg.DefaultDBUser}), nil
	})
	discovery := reloadingDiscovery{source: source}
	if got := discovery.ResolveDBUser("postgres", ""); got != "one" {
		t.Fatalf("user = %q, want one", got)
	}
	later := time.Now().Add(time.Second)
	writeTestFile(t, path, `{"defaultDbUser": "two"}`)
	setModTime(t, path, later)
	if got := discovery.ResolveDBUser("postgres", ""); got != "two" {
		t.Fatalf("user = %q, want the changed file's two", got)
	}
	writeTestFile(t, path, `{"defaultDbUser": `)
	setModTime(t, path, later.Add(time.Second))
	if got := discovery.ResolveDBUser("postgres", ""); got != "two" {
		t.Fatalf("user = %q, want the last valid two", got)
	}
}
