package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/orgconfig"
)

// testOrganization parses body as the organization of "org.json".
func testOrganization(t *testing.T, body string) *config.Organization {
	t.Helper()
	org, _, err := config.ParseOrganization("org.json", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return org
}

func TestLockedFlagRefused(t *testing.T) {
	cmd := NewRootCmd()
	if err := cmd.ParseFlags([]string{"--mode", "daemon"}); err != nil {
		t.Fatal(err)
	}
	err := lockedFlags(cmd.Flags(), testOrganization(t, `{"settings":{"mode":"cli"},"locked":["mode"]}`))
	if err == nil || !errors.Is(err, config.ErrLocked) || !strings.Contains(err.Error(), "--mode") || !strings.Contains(err.Error(), "org.json") {
		t.Fatalf("err = %v; want ErrLocked naming the flag and the source", err)
	}
}

func TestRegionFlagAllowedWhenLocked(t *testing.T) {
	cmd := NewRootCmd()
	if err := cmd.ParseFlags([]string{"--region", "us-east-1", "--db-user", "me"}); err != nil {
		t.Fatal(err)
	}
	org := testOrganization(t, `{"settings":{"aws":{"defaultRegion":"eu-west-3"},"modules":{"sql":{"defaultDbUser":"ro"}}},"locked":"*"}`)
	if err := lockedFlags(cmd.Flags(), org); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestNoticesNotRefreshedAfter24h(t *testing.T) {
	for age, want := range map[time.Duration]int{23 * time.Hour: 0, 25 * time.Hour: 1} {
		state := orgState{Org: testOrganization(t, testOrgBody), Source: "https://example.invalid/org.json", FetchedAt: testNow.Add(-age)}
		if got := organizationNotices(state, nil, testNow); len(got) != want {
			t.Errorf("%v old: notices = %q, want %d", age, got, want)
		}
	}
}

func TestNoticesIgnoredLockedLocalValue(t *testing.T) {
	org := testOrganization(t, testOrgBody)
	path := writeTempFile(t, "config.json", `{"aws": {"jumphostTag": "Env=local"}}`)
	settings, err := config.Settings(path, org)
	if err != nil {
		t.Fatal(err)
	}
	got := organizationNotices(orgState{Org: org}, settings, testNow)
	if len(got) != 1 || !strings.Contains(got[0], "aws.jumphostTag") || !strings.Contains(got[0], "Env=local") {
		t.Fatalf("notices = %q", got)
	}
}

func TestNoticesShadowHint(t *testing.T) {
	org := testOrganization(t, `{"settings":{"mode":"daemon","mcp":{"serverPrefix":"acme"}}}`)
	path := writeTempFile(t, "config.json", `{"mode": "daemon", "mcp": {"serverPrefix": "mine"}}`)
	settings, err := config.Settings(path, org)
	if err != nil {
		t.Fatal(err)
	}
	got := organizationNotices(orgState{Org: org}, settings, testNow)
	want := "1 local value repeats the organization's and hides its later changes: run `airlock config sync`"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("notices = %q", got)
	}
}

func TestDiffShownOnceThenCleared(t *testing.T) {
	env := testOrgEnv(t, nil)
	previous := orgEnvironment
	orgEnvironment = func() orgEnv { return env }
	t.Cleanup(func() { orgEnvironment = previous })
	source := "https://example.invalid/org.json"
	if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{
		Source: source, FetchedAt: testNow, Body: []byte(testOrgBody), Previous: []byte(`{"settings":{"aws":{"jumphostTag":"Role=old"}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)

	cmd := NewRootCmd()
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	printOrganizationNotices(cmd, &configSources{path: local, org: organizationFor(t.Context(), local)})
	if out := stderr.String(); !strings.Contains(out, "aws.jumphostTag") || !strings.Contains(out, "Role=old") {
		t.Fatalf("stderr:\n%s", out)
	}
	if state := organizationFor(t.Context(), local); state.Org == nil || len(state.Changes) != 0 {
		t.Fatalf("second resolve = %+v", state)
	}
}

func TestWizardOrganizationNoticeClearsTheDiff(t *testing.T) {
	env := testOrgEnv(t, nil)
	previous := orgEnvironment
	orgEnvironment = func() orgEnv { return env }
	t.Cleanup(func() { orgEnvironment = previous })
	source := "https://example.invalid/org.json"
	if err := orgconfig.SaveCache(env.cachePath, orgconfig.Cache{
		Source: source, FetchedAt: testNow.Add(-48 * time.Hour), Body: []byte(testOrgBody), Previous: []byte(`{"settings":{"aws":{"jumphostTag":"Role=old"}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	local := writeTempFile(t, "config.json", `{"organizationConfig": "`+source+`"}`)

	lines := takeWizardOrganization(&configSources{path: local, org: organizationFor(t.Context(), local)}).banner()
	text := strings.Join(lines, "\n")
	for _, want := range []string{"has not been read since", "changed:", "aws.jumphostTag", "Role=old"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lines lack %q:\n%s", want, text)
		}
	}
	if state := organizationFor(t.Context(), local); state.Org == nil || len(state.Changes) != 0 {
		t.Fatalf("second resolve = %+v, want the diff cleared", state)
	}
}

func TestWizardOrganizationPrintedOnExitUnlessShown(t *testing.T) {
	w := &wizardOrganization{notices: []string{"boom"}, diff: []string{"The organization config org.json changed:", "  ~ mode   cli → daemon"}}
	cmd := NewRootCmd()
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	w.printUnshown(cmd)
	if out := stderr.String(); out != "Warning: boom\nThe organization config org.json changed:\n  ~ mode   cli → daemon\n" {
		t.Fatalf("stderr = %q", out)
	}
	stderr.Reset()
	w.markShown()
	w.printUnshown(cmd)
	if stderr.Len() != 0 {
		t.Fatalf("printed after the wizard showed it: %q", stderr.String())
	}
}

func TestRefreshOrganizationKeepsCacheOnInvalidBody(t *testing.T) {
	server, hits := countingServer(t, `{"settings":{"agent":{"writes":"maybe"}}}`)
	env := testOrgEnv(t, server.Client())
	src, err := orgconfig.ParseSource(server.URL + "/org.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = orgconfig.SaveCache(env.cachePath, orgconfig.Cache{Source: src.Raw, Body: []byte(testOrgBody)}); err != nil {
		t.Fatal(err)
	}
	if err = refreshOrganization(t.Context(), env, src); err == nil || hits.Load() != 1 {
		t.Fatalf("err = %v, %d requests", err, hits.Load())
	}
	if cache, _ := orgconfig.LoadCache(env.cachePath).For(src.Raw); string(cache.Body) != testOrgBody {
		t.Fatalf("cache body = %s", cache.Body)
	}
}

func TestRootRefusesLockedFlagBeforeRunning(t *testing.T) {
	testConfigFile(t)
	useManagedFile(t, `{"settings":{"mode":"cli"},"locked":["mode"]}`)
	if _, err := runAirlock(t, "--mode", "daemon", "rds"); !errors.Is(err, config.ErrLocked) {
		t.Fatalf("err = %v; want ErrLocked", err)
	}
}

func TestOrganizationDiffLockOnlyChanges(t *testing.T) {
	unlocked := testOrganization(t, `{"settings":{"mode":"cli"}}`)
	locked := testOrganization(t, `{"settings":{"mode":"cli"},"locked":["mode"]}`)
	for name, tt := range map[string]struct {
		oldOrg, newOrg *config.Organization
		want           string
	}{
		"now locked":       {unlocked, locked, "cli (now locked by the organization)"},
		"no longer locked": {locked, unlocked, "cli (no longer locked)"},
	} {
		t.Run(name, func(t *testing.T) {
			lines := organizationDiff(&orgState{Source: "org.json", Changes: config.DiffOrganizations(tt.oldOrg, tt.newOrg)})
			if len(lines) < 2 || !strings.HasSuffix(lines[1], "mode   "+tt.want) {
				t.Fatalf("lines = %q", lines)
			}
		})
	}
}

func TestMCPAllowWritesFlagRefusedWhenLocked(t *testing.T) {
	testConfigFile(t)
	useManagedFile(t, `{"settings":{"agent":{"writes":false}},"locked":["agent.writes"]}`)
	for _, args := range [][]string{{"rds"}, {"direct", "legacy-billing"}, {"docker", "pg"}} {
		_, err := runAirlock(t, append([]string{"--mcp-allow-writes"}, args...)...)
		if !errors.Is(err, config.ErrLocked) || !strings.Contains(err.Error(), "--mcp-allow-writes") {
			t.Errorf("%v: err = %v; want ErrLocked naming the flag", args, err)
		}
	}
}

func TestWritesLocked(t *testing.T) {
	for body, want := range map[string]bool{
		`{"settings":{"agent":{"writes":false}},"locked":["agent.writes"]}`: true,
		`{"settings":{"agent":{"writes":true}},"locked":["agent.writes"]}`:  false, // never turns writes on
		`{"settings":{"agent":{"writes":false}}}`:                           false,
	} {
		org := testOrganization(t, body)
		cfg, err := config.Load("", org)
		if err != nil {
			t.Fatal(err)
		}
		if got := writesLocked(org, &cfg); got != want {
			t.Errorf("%s: writesLocked = %v, want %v", body, got, want)
		}
	}
	if cfg := config.Defaults(); writesLocked(nil, &cfg) {
		t.Error("no organization locks nothing")
	}
}

func TestDockerAllowWritesOffWhenLocked(t *testing.T) {
	cmd := NewRootCmd()
	if dockerAllowWrites(cmd, false, true) || !dockerAllowWrites(cmd, false, false) {
		t.Fatal("a Docker database writes by default, unless the organization locks writes")
	}
}
