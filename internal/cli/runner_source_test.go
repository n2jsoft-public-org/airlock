package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/orgconfig"
)

func TestRunnerSourceReloadsChangedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"mcpServerPrefix": "one"}`)
	var built []string
	source := newRunnerSource(path, nil, func(cfg config.Config, _ *config.Organization) (sessionsRunner, error) {
		built = append(built, cfg.MCPServerPrefix)
		return nil, nil
	})
	if _, err := source.Current(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Current(); err != nil || len(built) != 1 {
		t.Fatalf("an unchanged file must not rebuild: %v, %v", built, err)
	}
	later := time.Now().Add(time.Second)
	writeTestFile(t, path, `{"mcpServerPrefix": "two"}`)
	setModTime(t, path, later)
	if _, err := source.Current(); err != nil || len(built) != 2 || built[1] != "two" {
		t.Fatalf("built = %v, err = %v", built, err)
	}
	writeTestFile(t, path, `{"mcpServerPrefix": `)
	setModTime(t, path, later.Add(time.Second))
	if _, err := source.Current(); err == nil {
		t.Fatal("an invalid file fails the opening")
	}
	writeTestFile(t, path, `{"mcpServerPrefix": "two"}`)
	setModTime(t, path, later.Add(2*time.Second))
	if _, err := source.Current(); err != nil {
		t.Fatalf("a fixed file applies again: %v", err)
	}
}

func setModTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSourceLastKeepsTheValidRunner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"defaultDbUser": "one"}`)
	source := newRunnerSource(path, nil, func(cfg config.Config, _ *config.Organization) (*jumphost.Runner, error) {
		return jumphost.NewRunner(&jumphost.RunnerOptions{DefaultDBUser: cfg.DefaultDBUser}), nil
	})
	if _, ok := source.Last(); ok {
		t.Fatal("Last reports a runner before the first opening")
	}
	first, err := source.Current()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, `{"defaultDbUser": `)
	setModTime(t, path, time.Now().Add(time.Second))
	if _, err = source.Current(); err == nil {
		t.Fatal("an invalid file fails the opening")
	}
	if last, ok := source.Last(); !ok || last != first {
		t.Fatalf("Last = %p, %v; want the runner of the valid file", last, ok)
	}
}

func TestRunnerSourceNeverReturnsANilRunnerInterface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"defaultDbUser": `)
	source := newRunnerSource(path, nil, func(config.Config, *config.Organization) (*jumphost.Runner, error) {
		return jumphost.NewRunner(&jumphost.RunnerOptions{}), nil
	})
	runner, err := source.Source()()
	if err == nil || runner != nil {
		t.Fatalf("runner = %v, err = %v; want a nil interface and an error", runner, err)
	}
}

func TestRunnerSourceRebuildsOnOrganizationCacheChange(t *testing.T) {
	env := testOrgEnv(t, &http.Client{Transport: refuseRequests{}})
	source := "https://example.invalid/org.json"
	path := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`", "mcp": {"serverPrefix": "local"}}`)
	saveOrg := func(body string, at time.Time) {
		t.Helper()
		if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: source, FetchedAt: testNow, Body: []byte(body)}); err != nil {
			t.Fatal(err)
		}
		setModTime(t, env.cachePath, at)
	}
	saveOrg(`{"settings":{"agent":{"autoConnect":"none"}}}`, time.Now())
	org := resolveOrganization(t.Context(), env, path).Org
	type built struct {
		autoConnect, prefix string
		locked              bool
	}
	var builds []built
	runners := newRunnerSource(path, org, withOverrides(config.Overrides{MCPServerPrefix: new("flag")}, func(cfg config.Config, org *config.Organization) (sessionsRunner, error) {
		builds = append(builds, built{cfg.AgentAutoConnect, cfg.MCPServerPrefix, writesLocked(org, &cfg)})
		return nil, nil
	})).followOrganization(env)
	if _, err := runners.Current(); err != nil {
		t.Fatal(err)
	}
	if _, err := runners.Current(); err != nil || len(builds) != 1 {
		t.Fatalf("an unchanged cache must not rebuild: %+v, %v", builds, err)
	}
	saveOrg(`{"settings":{"agent":{"autoConnect":"all","writes":false},"mcp":{"serverPrefix":"org"}},"locked":["agent.writes","mcp.serverPrefix"]}`, time.Now().Add(time.Second))
	if _, err := runners.Current(); err != nil || len(builds) != 2 {
		t.Fatalf("builds = %+v, err = %v; want a rebuild", builds, err)
	}
	if got := builds[1]; got != (built{"all", "org", true}) {
		t.Fatalf("rebuilt = %+v; want the new organization, its lock winning over the flag", got)
	}
}

func TestRunnerSourceRebuildsOnManagedFileOrFileSourceChange(t *testing.T) {
	unlocked, locked := `{"settings":{"agent":{"writes":false}}}`, `{"settings":{"agent":{"writes":false}},"locked":["agent.writes"]}`
	orgFile := writeTempFile(t, "org.json", unlocked)
	for name, tt := range map[string]struct {
		local, changed string
		managed        bool
	}{
		"managed file": {local: `{}`, managed: true},
		"file source":  {local: `{"organizationConfig": "` + orgFile + `"}`, changed: orgFile},
	} {
		t.Run(name, func(t *testing.T) {
			writeTestFile(t, orgFile, unlocked)
			env := testOrgEnv(t, &http.Client{Transport: refuseRequests{}})
			if tt.managed {
				env.managedPath = writeTempFile(t, "managed.json", unlocked)
				tt.changed = env.managedPath
			}
			path := writeTempFile(t, "config.json", tt.local)
			var locks []bool
			runners := newRunnerSource(path, nil, func(cfg config.Config, org *config.Organization) (sessionsRunner, error) {
				locks = append(locks, writesLocked(org, &cfg))
				return nil, nil
			}).followOrganization(env)
			for range 2 {
				if _, err := runners.Current(); err != nil {
					t.Fatal(err)
				}
			}
			writeTestFile(t, tt.changed, locked)
			setModTime(t, tt.changed, time.Now().Add(time.Second))
			if _, err := runners.Current(); err != nil || !slices.Equal(locks, []bool{false, true}) {
				t.Fatalf("locks = %v, err = %v; want one build, then a rebuild with the lock", locks, err)
			}
		})
	}
}
