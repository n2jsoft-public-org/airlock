package mcpserve

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeCodexConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexAddKeepsTheRestOfTheFile(t *testing.T) {
	useIsolatedLocks(t)
	original := "# my settings\nmodel = \"gpt-5\"\n\n[mcp_servers.docs]\ncommand = \"docs-mcp\" # local\n"
	path := writeCodexConfig(t, original)

	if err := (codexFormat{}).add(path, sampleEntry("http://127.0.0.1:1/mcp/a")); err != nil {
		t.Fatal(err)
	}
	want := original + "\n[mcp_servers.acme-dev-app]\nurl = \"http://127.0.0.1:1/mcp/a\"\nhttp_headers = { Authorization = \"Bearer tok\" }\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("config.toml =\n%s\nwant\n%s", got, want)
	}

	removed, err := (codexFormat{}).remove(path, sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil || !removed {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("config.toml after removal =\n%q\nwant\n%q", got, original)
	}
}

func TestCodexAddReplacesTheServerAndItsSubTables(t *testing.T) {
	useIsolatedLocks(t)
	path := writeCodexConfig(t, "[mcp_servers.\"acme-dev-app\"]\nurl = \"http://old\"\n\n[mcp_servers.acme-dev-app.tools.query]\nenabled = false\n\n[profiles.fast]\nmodel = \"x\"\n")

	if err := (codexFormat{}).add(path, sampleEntry("http://127.0.0.1:2/mcp/new")); err != nil {
		t.Fatal(err)
	}
	want := "[profiles.fast]\nmodel = \"x\"\n\n[mcp_servers.acme-dev-app]\nurl = \"http://127.0.0.1:2/mcp/new\"\nhttp_headers = { Authorization = \"Bearer tok\" }\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("config.toml =\n%s\nwant\n%s", got, want)
	}
}

func TestCodexRemoveKeepsAServerReplacedByAnotherSession(t *testing.T) {
	useIsolatedLocks(t)
	path := writeCodexConfig(t, "")
	if err := (codexFormat{}).add(path, sampleEntry("http://127.0.0.1:2/mcp/second")); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, path)

	removed, err := (codexFormat{}).remove(path, sampleEntry("http://127.0.0.1:1/mcp/first"))
	if err != nil || removed {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	if readFile(t, path) != before {
		t.Fatal("the other session's server must stay")
	}
}

func TestCodexRefusesInvalidFilesAndUneditableDefinitions(t *testing.T) {
	useIsolatedLocks(t)
	invalid := "model = \n"
	path := writeCodexConfig(t, invalid)
	if err := (codexFormat{}).add(path, sampleEntry("http://127.0.0.1:1/mcp/a")); err == nil || !strings.Contains(err.Error(), "not valid TOML") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, path) != invalid {
		t.Fatal("an invalid file must be left alone")
	}

	inline := "[mcp_servers]\nacme-dev-app = { url = \"http://elsewhere\" }\n"
	path = writeCodexConfig(t, inline)
	if err := (codexFormat{}).add(path, sampleEntry("http://127.0.0.1:1/mcp/a")); err == nil || !strings.Contains(err.Error(), "cannot edit") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, path) != inline {
		t.Fatal("an uneditable file must be left alone")
	}
}

func TestTOMLHeader(t *testing.T) {
	cases := map[string][]string{
		"[mcp_servers.app]\n":               {"mcp_servers", "app"},
		"  [ mcp_servers . \"a.b\" ] # x\n": {"mcp_servers", "a.b"},
		"[[mcp_servers.'lit'.list]]\n":      {"mcp_servers", "lit", "list"},
	}
	for line, want := range cases {
		if got, ok := tomlHeader(line); !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("tomlHeader(%q) = %v, %v, want %v", line, got, ok, want)
		}
	}
	for _, line := range []string{"model = 1\n", "[1, 2],\n", "[a.b] trailing\n", "[a..b]\n", "\n"} {
		if got, ok := tomlHeader(line); ok {
			t.Fatalf("tomlHeader(%q) = %v, want no header", line, got)
		}
	}
}

func TestTOMLKeyQuotesWhenNeeded(t *testing.T) {
	if got := tomlKey("acme-dev_1"); got != "acme-dev_1" {
		t.Fatalf("bare key = %s", got)
	}
	if got := tomlKey("a.b \"c\""); got != `"a.b \"c\""` {
		t.Fatalf("quoted key = %s", got)
	}
}
