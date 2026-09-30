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

// saveRecent remembers connections in the connections.json of a fresh XDG_CONFIG_HOME.
func saveRecent(t *testing.T, connections ...jumphost.SavedConnection) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := jumphost.NewPrefsStore(filepath.Join(root, "airlock", "connections.json"))
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
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte("#!/bin/sh\nprintf 'prod\\ndev\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := complete(t, "--profile", ""); strings.Join(got, ",") != "dev,prod" {
		t.Fatalf("candidates = %q, want the AWS profiles", got)
	}
}
