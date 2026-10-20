package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/spf13/cobra"
)

// saveRecent remembers connections in the saved-connections.json of a fresh XDG_CONFIG_HOME.
func saveRecent(t *testing.T, connections ...jumphost.SavedConnection) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := jumphost.NewPrefsStore(filepath.Join(root, "airlock", "saved-connections.json"))
	for idx := range connections {
		if err := store.Save(connections[idx].Ref, &connections[idx].Prefs); err != nil {
			t.Fatal(err)
		}
	}
}

// complete asks the shell completion of args and returns its candidates, one per line.
func complete(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{cobra.ShellCompRequestCmd}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var candidates []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, ":") {
			candidates = append(candidates, line)
		}
	}
	return candidates
}

func recent(resourceType, name, profile, region string, port int) jumphost.SavedConnection {
	return jumphost.SavedConnection{
		Ref:   jumphost.TargetRef{Account: "111111111111", Region: region, Type: resourceType, Name: name},
		Prefs: jumphost.ConnectionPrefs{Profile: profile, LocalPort: port, UpdatedAt: time.Now()},
	}
}

func TestCompletesResourceTypes(t *testing.T) {
	saveRecent(t)
	got := strings.Join(complete(t, ""), "\n")
	for _, want := range []string{"rds\t", "eks\t", "elasticache\t"} {
		if !strings.Contains(got, want) {
			t.Fatalf("candidates = %q, want %q", got, want)
		}
	}
}

func TestCompletionLeavesOutDisabledModules(t *testing.T) {
	saveRecent(t)
	disableModules(t, "sql,s3")
	got := strings.Join(complete(t, ""), "\n")
	for _, gone := range []string{"rds\t", "direct\t", "docker\t", "s3\t"} {
		if strings.Contains(got, gone) {
			t.Fatalf("candidates = %q, want no %q", got, gone)
		}
	}
	if !strings.Contains(got, "eks\t") {
		t.Fatalf("candidates = %q, want eks", got)
	}
}

func TestCompletesRecentTargetsOfTheType(t *testing.T) {
	saveRecent(t,
		recent("rds", "archive", "dev", "eu-west-3", 5432),
		recent("rds", "billing", "prod", "eu-west-1", 5433),
		recent("eks", "platform", "dev", "eu-west-3", 9444),
	)
	got := strings.Join(complete(t, "rds", ""), "\n")
	if !strings.Contains(got, "archive\tdev · eu-west-3") || !strings.Contains(got, "billing\tprod · eu-west-1") || strings.Contains(got, "platform") {
		t.Fatalf("candidates = %q, want the RDS targets with their profile and region", got)
	}

	if got := complete(t, "--profile", "prod", "rds", ""); len(got) != 1 || !strings.HasPrefix(got[0], "billing") {
		t.Fatalf("candidates = %q, want the targets of --profile only", got)
	}
	if got := complete(t, "--region", "eu-west-3", "rds", ""); len(got) != 1 || !strings.HasPrefix(got[0], "archive") {
		t.Fatalf("candidates = %q, want the targets of --region only", got)
	}
}

func TestCompletesTheSavedPort(t *testing.T) {
	saveRecent(t, recent("rds", "archive", "dev", "eu-west-3", 5433))
	if got := complete(t, "rds", "archive", ""); len(got) != 1 || !strings.HasPrefix(got[0], "5433") {
		t.Fatalf("candidates = %q, want the port used last", got)
	}
}

func TestCompletesRegions(t *testing.T) {
	saveRecent(t)
	if got := strings.Join(complete(t, "--region", ""), "\n"); !strings.Contains(got, "eu-west-3\tEurope (Paris)") {
		t.Fatalf("candidates = %q, want the region codes with their names", got)
	}
}

func TestCompletesProfiles(t *testing.T) {
	saveRecent(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("[profile prod]\n\n[profile dev]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	if got := complete(t, "--profile", ""); strings.Join(got, ",") != "dev,prod" {
		t.Fatalf("candidates = %q, want the AWS profiles", got)
	}
}

func TestCompletesTheDirectType(t *testing.T) {
	testDatabases(t)
	if got := strings.Join(complete(t, ""), "\n"); !strings.Contains(got, "direct\tDirect database") {
		t.Fatalf("candidates = %q, want the direct type", got)
	}
}

func TestCompletesDirectDatabaseNames(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	putDatabase(t, store, &archive)
	want := "legacy-billing\tsqlserver · billing.db.internal:1433"

	for _, args := range [][]string{{"direct", ""}, {"db", "edit", ""}, {"db", "test", ""}, {"db", "export", ""}} {
		if got := strings.Join(complete(t, args...), "\n"); !strings.Contains(got, want) || !strings.Contains(got, "archive\tpostgres · archive.internal:5432") {
			t.Fatalf("%v: candidates = %q", args, got)
		}
	}
	removable := strings.Join(complete(t, "connection", "remove", ""), "\n")
	if !strings.Contains(removable, "legacy-billing\tdirect database legacy-billing (billing.db.internal:1433)") || !strings.Contains(removable, "archive\tdirect database archive") {
		t.Fatalf("connection remove: candidates = %q", removable)
	}
	if got := complete(t, "direct", "legacy-billing", ""); len(got) != 0 {
		t.Fatalf("candidates after the name = %q, want none (no local port)", got)
	}
	if got := complete(t, "db", "edit", "legacy-billing", ""); len(got) != 0 {
		t.Fatalf("db edit takes one name: candidates = %q", got)
	}
	if got := strings.Join(complete(t, "db", "export", "legacy-billing", ""), "\n"); strings.Contains(got, "legacy-billing") || !strings.Contains(got, "archive") {
		t.Fatalf("db export candidates = %q, want the names not given yet", got)
	}
}

func TestCompletionNeverReadsARemoteOrganization(t *testing.T) {
	path := testConfigFile(t)
	server, hits := countingServer(t, testOrgBody)
	useOrgEnv(t, testOrgEnv(t, server.Client()))
	writeTestFile(t, path, `{"organizationConfig": "`+server.URL+`/org.json"}`)
	if got := complete(t, ""); len(got) == 0 {
		t.Fatal("no candidates")
	}
	if hits.Load() != 0 {
		t.Fatalf("completion read the organization source %d times, want only the cache", hits.Load())
	}
}
