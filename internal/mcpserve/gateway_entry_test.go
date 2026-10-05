package mcpserve

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestIdenticalEntryIsNotRewritten(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)
	sel := mustParse(t, "claude:user,codex:user")
	entry := ClientEntry{Name: "airlock", URL: "http://127.0.0.1:7821/mcp", Token: "abc"}
	if _, err := RegisterClients("", sel, entry); err != nil {
		t.Fatal(err)
	}
	claude, codex := filepath.Join(home, ".claude.json"), filepath.Join(home, ".codex", "config.toml")
	old := time.Now().Add(-time.Hour)
	for _, path := range []string{claude, codex} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RegisterClients("", sel, entry); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{claude, codex} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(old) {
			t.Errorf("%s was rewritten for an identical entry", path)
		}
	}
	entry.Token = "def"
	if _, err := RegisterClients("", sel, entry); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(claude)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Equal(old) {
		t.Fatal("a changed token must be written")
	}
}

func TestEntryFilesFindsTheEntryWithItsURL(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)
	entry := ClientEntry{Name: "airlock", URL: "http://127.0.0.1:7821/mcp", Token: "abc"}
	if _, err := RegisterClients("", mustParse(t, "claude:user,cursor:user"), entry); err != nil {
		t.Fatal(err)
	}
	files, err := EntryFiles(mustParse(t, "claude:user,codex:user,cursor:user"), entry)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, ".claude.json"), filepath.Join(home, ".cursor", "mcp.json")}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	other := entry
	other.URL = "http://127.0.0.1:9999/mcp"
	if files, err = EntryFiles(mustParse(t, "claude:user"), other); err != nil || len(files) != 0 {
		t.Fatalf("an entry of another URL is not ours: %v, %v", files, err)
	}
}
