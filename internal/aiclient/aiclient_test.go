package aiclient

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDefaultsToAutoDetect(t *testing.T) {
	for _, input := range []string{"", "  ", " , ", "auto", " AUTO "} {
		sel, err := Parse(input)
		if err != nil || !reflect.DeepEqual(sel, AutoDetect) {
			t.Fatalf("Parse(%q) = %+v, %v, want auto-detect", input, sel, err)
		}
	}
}

func TestParseClientsAndScopes(t *testing.T) {
	sel, err := Parse(" Claude:user, codex , vscode:project,codex:project, auto ")
	if err != nil {
		t.Fatal(err)
	}
	want := Selection{Detect: true, Targets: []Target{
		{Client: Claude, Scope: ScopeUser}, {Client: Codex, Scope: ScopeProject}, {Client: VSCode, Scope: ScopeProject},
	}}
	if !reflect.DeepEqual(sel, want) {
		t.Fatalf("sel = %+v, want %+v", sel, want)
	}
	if got := sel.String(); got != "auto,claude:user,codex,vscode" {
		t.Fatalf("String() = %q", got)
	}
}

func TestParseNone(t *testing.T) {
	sel, err := Parse("none")
	if err != nil || sel.Detect || len(sel.Targets) != 0 {
		t.Fatalf("sel = %+v, %v", sel, err)
	}
	if sel.String() != None {
		t.Fatalf("String() = %q", sel.String())
	}
	if _, err := Parse("none,claude"); err == nil || !strings.Contains(err.Error(), "none") {
		t.Fatalf("none with other clients must be refused: %v", err)
	}
}

func TestParseRejectsUnknownClientsAndScopes(t *testing.T) {
	if _, err := Parse("claude,emacs"); err == nil || !strings.Contains(err.Error(), `"emacs"`) || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse("codex:global"); err == nil || !strings.Contains(err.Error(), `"global"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSelectionUserOnly(t *testing.T) {
	sel, err := Parse("auto,claude:user,codex,cursor:user")
	if err != nil {
		t.Fatal(err)
	}
	if got := sel.UserOnly().String(); got != "claude:user,cursor:user" {
		t.Fatalf("UserOnly = %q", got)
	}
	if got := AutoDetect.UserOnly().String(); got != None {
		t.Fatalf("auto alone has no user target: %q", got)
	}
	if got := AllUser().String(); got != "claude:user,vscode:user,codex:user,cursor:user,gemini:user" {
		t.Fatalf("AllUser = %q", got)
	}
}
