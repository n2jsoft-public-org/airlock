package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsInstallFollowsMCPClients(t *testing.T) {
	writeTestFile(t, testConfigFile(t), `{"mcp": {"clients": "auto,codex"}}`)
	project := t.TempDir()
	t.Chdir(project)
	if err := os.Mkdir(filepath.Join(project, ".claude"), 0o750); err != nil {
		t.Fatal(err)
	}

	out, err := runAirlock(t, "skills", "install")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".claude/skills/airlock/SKILL.md", ".agents/skills/airlock/SKILL.md"} {
		if !strings.Contains(out, "written to "+filepath.FromSlash(want)) {
			t.Fatalf("output lacks %s:\n%s", want, out)
		}
	}
}

func TestSkillsInstallRefusesAnEmptySelection(t *testing.T) {
	testConfigFile(t)
	t.Chdir(t.TempDir())
	if _, err := runAirlock(t, "skills", "install"); err == nil || !strings.Contains(err.Error(), "selects no AI client") {
		t.Fatalf("err = %v, want the empty selection refused", err)
	}
}

func TestSkillsInstallWritesTheNamedClients(t *testing.T) {
	testConfigFile(t)
	project := t.TempDir()
	t.Chdir(project)
	out, err := runAirlock(t, "skills", "install", "gemini")
	if err != nil || strings.Contains(out, ".claude") {
		t.Fatalf("out = %q, %v, want only .agents/skills", out, err)
	}
	if _, err = os.Stat(filepath.Join(project, ".agents", "skills", "airlock", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
