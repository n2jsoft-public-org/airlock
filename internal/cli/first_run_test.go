package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/config"
)

func TestFirstRunOnlyWithoutConfigFileOnTheDashboard(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "airlock", "config.json")
	existing := writeTempFile(t, "config.json", "{}\n")
	for _, tc := range []struct {
		name      string
		path      string
		dashboard bool
		want      bool
	}{
		{"first launch", missing, true, true},
		{"config exists", existing, true, false},
		{"no terminal or a command", missing, false, false},
		{"no config directory", "", true, false},
	} {
		if got := firstRunNeeded(tc.path, tc.dashboard); got != tc.want {
			t.Errorf("%s: firstRunNeeded = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSetupNeedsATerminal(t *testing.T) {
	path := testConfigFile(t)
	if _, err := runAirlock(t, "setup"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("err = %v, want a refusal without a terminal", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("setup without a terminal wrote %s", path)
	}
}

func TestSkippedFirstRunFileIsAValidEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "config.json")
	if err := config.DropKeys(path, nil); err != nil {
		t.Fatal(err)
	}
	if firstRunNeeded(path, true) {
		t.Fatalf("the wizard opens again after a skip")
	}
	if _, err := config.Load(path, nil); err != nil {
		t.Fatalf("the skipped config does not load: %v", err)
	}
}

func TestReadOrganizationValidatesTheSource(t *testing.T) {
	env := testOrgEnv(t, nil)
	valid := writeTempFile(t, "org.json", `{"settings": {"mode": "daemon"}, "locked": ["mode"]}`)
	org, err := readOrganization(context.Background(), env, valid)
	if err != nil || !org.Locked("mode") {
		t.Fatalf("org = %v err = %v, want mode locked", org, err)
	}
	invalid := writeTempFile(t, "org.json", `{"locked": ["mode"]}`)
	if _, err = readOrganization(context.Background(), env, invalid); err == nil {
		t.Fatalf("a lock without a value is accepted")
	}
	if _, err = readOrganization(context.Background(), env, filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatalf("a missing file is accepted")
	}
	if org, err = readOrganization(context.Background(), env, ""); org != nil || err != nil {
		t.Fatalf("org = %v err = %v, want none for an empty source", org, err)
	}
}
