package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/config"
)

// runAirlockWithInput is runAirlock with input as standard input.
func runAirlockWithInput(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd()
	var out, stderr strings.Builder
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// shareString exports a config file holding content.
func shareString(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, content)
	shared, err := config.Export(path)
	if err != nil {
		t.Fatal(err)
	}
	return shared
}

func readConfigFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestConfigExportPrintsOnlyTheString(t *testing.T) {
	path := testConfigFile(t)
	writeTestFile(t, path, `{"defaultRegion": "eu-west-3"}`)
	out, err := runAirlock(t, "config", "export")
	if err != nil {
		t.Fatal(err)
	}
	shared := strings.TrimSuffix(out, "\n")
	if !strings.HasPrefix(shared, "airlock1:") || strings.Contains(shared, "\n") {
		t.Fatalf("out = %q, want a single config string", out)
	}
	if want, exportErr := config.Export(path); exportErr != nil || shared != want {
		t.Fatalf("out = %q, want %q (%v)", shared, want, exportErr)
	}
}

func TestConfigImportShowsTheChangesAndAbortsWithoutYes(t *testing.T) {
	shared := shareString(t, `{"jumphostTag": "Env=bastion", "defaultRegion": "eu-west-3", "mcpServerPrefix": "acme"}`)
	path := testConfigFile(t)
	local := `{"jumphostTag": "Usage=jumphost", "mcpServerPrefix": "acme"}`
	writeTestFile(t, path, local)

	for _, answer := range []string{"n\n", "", "maybe\n"} {
		out, err := runAirlockWithInput(t, answer, "config", "import", shared)
		if err == nil || !strings.Contains(err.Error(), "not applied") {
			t.Fatalf("answer %q: err = %v, want not applied", answer, err)
		}
		for _, want := range []string{
			"Import into " + path,
			"~ jumphostTag", "Usage=jumphost → Env=bastion", "Tag (Key=Value) of the running SSM jumphost EC2 instance",
			"+ defaultRegion", "(none, default) → eu-west-3",
			"= mcpServerPrefix", "acme (unchanged)",
			"Apply these changes to " + path + "? [y/N]",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("answer %q: output lacks %q:\n%s", answer, want, out)
			}
		}
		if got := readConfigFile(t, path); got != local {
			t.Fatalf("answer %q: the file changed: %s", answer, got)
		}
	}
}

func TestConfigImportAppliesOnYes(t *testing.T) {
	shared := shareString(t, `{"defaultRegion": "eu-west-3"}`)
	path := testConfigFile(t)

	out, err := runAirlockWithInput(t, "y\n", "config", "import", shared)
	if err != nil || !strings.Contains(out, "Applied 1 change to "+path) {
		t.Fatalf("out = %q, %v", out, err)
	}
	if cfg, loadErr := config.Load(path); loadErr != nil || cfg.DefaultRegion != "eu-west-3" {
		t.Fatalf("cfg = %+v, %v", cfg, loadErr)
	}
}

func TestConfigImportYesFlagSkipsTheQuestion(t *testing.T) {
	shared := shareString(t, `{"defaultRegion": "eu-west-3", "mcpServerPrefix": "acme"}`)
	path := testConfigFile(t)

	out, err := runAirlockWithInput(t, "", "config", "import", "--yes", shared)
	if err != nil || strings.Contains(out, "[y/N]") || !strings.Contains(out, "Applied 2 changes") {
		t.Fatalf("out = %q, %v, want applied without a question", out, err)
	}
	if cfg, loadErr := config.Load(path); loadErr != nil || cfg.MCPServerPrefix != "acme" {
		t.Fatalf("cfg = %+v, %v", cfg, loadErr)
	}
}

func TestConfigImportReplaceShowsRemovedKeys(t *testing.T) {
	shared := shareString(t, `{"defaultRegion": "eu-west-3"}`)
	path := testConfigFile(t)
	writeTestFile(t, path, `{"defaultDbUser": "acme_ro"}`)

	out, err := runAirlockWithInput(t, "y\n", "config", "import", "--replace", shared)
	if err != nil || !strings.Contains(out, "- defaultDbUser") || !strings.Contains(out, "acme_ro → (none, default)") {
		t.Fatalf("out = %q, %v", out, err)
	}
	if got := readConfigFile(t, path); strings.Contains(got, "defaultDbUser") {
		t.Fatalf("--replace kept a local key: %s", got)
	}
}

func TestConfigImportWithNothingToChange(t *testing.T) {
	shared := shareString(t, `{"defaultRegion": "eu-west-3"}`)
	path := testConfigFile(t)
	writeTestFile(t, path, `{"defaultRegion": "eu-west-3"}`)

	out, err := runAirlockWithInput(t, "", "config", "import", shared)
	if err != nil || !strings.Contains(out, "Nothing to change") || strings.Contains(out, "[y/N]") {
		t.Fatalf("out = %q, %v", out, err)
	}
}

func TestConfigImportRefusesADamagedString(t *testing.T) {
	shared := shareString(t, `{"defaultRegion": "eu-west-3"}`)
	testConfigFile(t)
	if _, err := runAirlockWithInput(t, "y\n", "config", "import", shared[:len(shared)-3]); err == nil || !strings.Contains(err.Error(), "not a valid airlock config string") {
		t.Fatalf("err = %v", err)
	}
}
