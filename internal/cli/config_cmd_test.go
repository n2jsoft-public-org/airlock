package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/config"
)

// runAirlock runs the root command with args and returns what it printed on standard output.
func runAirlock(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd()
	var out, stderr strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// testConfigFile points the config directory to a temporary one and returns its config file.
func testConfigFile(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	return filepath.Join(root, "airlock", "config.json")
}

func TestConfigPathPrintsTheFile(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "path")
	if err != nil || out != path+"\n" {
		t.Fatalf("out = %q, %v, want %q", out, err, path)
	}
}

func TestConfigShowWithoutFileListsTheDefaults(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{path, "not created: run `airlock config init`", "jumphostTag", "Usage=jumphost", "default", "defaultRegion", "(none)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigShowTellsFileValuesApart(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"defaultRegion": "eu-west-3"}`)
	out, err := runAirlock(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "not created") {
		t.Fatalf("the file exists:\n%s", out)
	}
	var regionLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "defaultRegion") {
			regionLine = line
		}
	}
	if fields := strings.Fields(regionLine); len(fields) != 3 || fields[1] != "eu-west-3" || fields[2] != "file" {
		t.Fatalf("defaultRegion line = %q, want its value and the file source", regionLine)
	}
}

func TestConfigShowJSONPrintsEffectiveValues(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"defaultRegion": "eu-west-3"}`)
	out, err := runAirlock(t, "config", "show", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(out), &values); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if values["defaultRegion"] != "eu-west-3" || values["jumphostTag"] != "Usage=jumphost" || len(values) != len(config.Keys) {
		t.Fatalf("values = %v", values)
	}
}

func TestConfigShowReportsAnInvalidFile(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"jumphost": "x"}`)
	if _, err := runAirlock(t, "config", "show"); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the invalid file named", err)
	}
}

func TestConfigInitWritesTheTemplateOnce(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "init")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Wrote " + path, "jumphostTag", "readOnlySecretTag", "`airlock config set <key> <value>`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if cfg, loadErr := config.Load(path); loadErr != nil || cfg != config.Defaults() {
		t.Fatalf("template = %+v, %v", cfg, loadErr)
	}

	if _, err = runAirlock(t, "config", "init"); err == nil || err.Error() != path+" already exists: use --force to overwrite" {
		t.Fatalf("second init err = %v", err)
	}

	out, err = runAirlock(t, "config", "init", "--force")
	if err != nil || !strings.Contains(out, path+".bak") {
		t.Fatalf("forced init = %q, %v, want the backup named", out, err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("no backup: %v", err)
	}
}

func TestConfigSetAndUnset(t *testing.T) {
	path := testConfigFile(t)
	out, err := runAirlock(t, "config", "set", "jumphostTag", "Env=bastion")
	if err != nil || !strings.Contains(out, "jumphostTag = Env=bastion") {
		t.Fatalf("set = %q, %v", out, err)
	}
	if cfg, loadErr := config.Load(path); loadErr != nil || cfg.JumphostTag.String() != "Env=bastion" {
		t.Fatalf("cfg = %+v, %v", cfg, loadErr)
	}

	if _, err = runAirlock(t, "config", "set", "jumphostTag", "Env"); err == nil {
		t.Fatal("a malformed tag must be refused")
	}
	if _, err = runAirlock(t, "config", "set", "jumphostTag"); err == nil {
		t.Fatal("set needs a key and a value")
	}

	out, err = runAirlock(t, "config", "unset", "jumphostTag")
	if err != nil || !strings.Contains(out, "the default applies") {
		t.Fatalf("unset = %q, %v", out, err)
	}
	out, err = runAirlock(t, "config", "unset", "jumphostTag")
	if err != nil || !strings.Contains(out, "jumphostTag was not set") {
		t.Fatalf("second unset = %q, %v", out, err)
	}
}

func TestRootStillTakesResourceArguments(t *testing.T) {
	cmd := NewRootCmd()
	found, args, err := cmd.Find([]string{"rds", "archive", "5433"})
	if err != nil || found != cmd || strings.Join(args, " ") != "rds archive 5433" {
		t.Fatalf("Find = %s %v %v, want the root command with its arguments", found.Name(), args, err)
	}
	found, _, err = cmd.Find([]string{"config", "show"})
	if err != nil || found.Name() != "show" {
		t.Fatalf("Find(config show) = %s, %v", found.Name(), err)
	}
}
