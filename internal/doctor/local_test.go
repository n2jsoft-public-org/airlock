package doctor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWritePrintsTheFixUnderEveryNonOkLine(t *testing.T) {
	var b strings.Builder
	results := []Result{ok("a", "", "fine"), warning("b", "", "meh", "do x"), failure("c", "", "bad", "do y")}
	if err := Write(&b, results); err != nil {
		t.Fatal(err)
	}
	want := "✓ fine\n! meh\n    do x\n✗ bad\n    do y\n"
	if b.String() != want {
		t.Fatalf("Write = %q, want %q", b.String(), want)
	}
	if !Failed(results) || Failed(results[:2]) {
		t.Fatal("Failed must report the failure only")
	}
}

// writeTool writes an executable script named name in dir.
func writeTool(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil { // #nosec G306 -- a test executable.
		t.Fatal(err)
	}
	return path
}

func TestOtherBinariesFindsCopiesOtherThanSelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	selfDir, otherDir, emptyDir := t.TempDir(), t.TempDir(), t.TempDir()
	self := writeTool(t, selfDir, "airlock", "")
	pathEnv := strings.Join([]string{selfDir, emptyDir, selfDir}, string(os.PathListSeparator))
	if r := OtherBinaries(self, pathEnv); r.Status != OK {
		t.Fatalf("OtherBinaries = %+v, want ok", r)
	}
	other := writeTool(t, otherDir, "airlock", "")
	r := OtherBinaries(self, pathEnv+string(os.PathListSeparator)+otherDir)
	if r.Status != Warning || !strings.Contains(r.Message, other) || strings.Contains(r.Message, self+",") {
		t.Fatalf("OtherBinaries = %+v, want %s only", r, other)
	}
}

func TestTunnelToolsReportsVersionsAndMissingTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	writeTool(t, dir, "aws", "echo 'aws-cli/2.0.0 Python/3'")

	results := TunnelTools(context.Background(), true)
	if len(results) != 2 || results[0].Status != OK || !strings.Contains(results[0].Message, "aws-cli/2.0.0") ||
		results[1].Status != Failure || !strings.Contains(results[1].Fix, "session-manager-plugin") {
		t.Fatalf("results = %+v, want aws ok and the plugin missing", results)
	}
	if results = TunnelTools(context.Background(), false); results[1].Status != Warning {
		t.Fatalf("results = %+v, want a warning when no tunnel module is enabled", results)
	}
}

func TestStatusOfPlainChecks(t *testing.T) {
	cases := []struct {
		name string
		got  Result
		want Status
	}{
		{"version", Version("v1.0.0", "", nil), OK},
		{"version available", Version("v1.0.0", "v1.1.0", nil), Warning},
		{"version unchecked", Version("v1.0.0", "", errors.New("offline")), Warning},
		{"config", Config("c.json", nil, nil), OK},
		{"config invalid", Config("c.json", errors.New("bad"), nil), Failure},
		{"config deprecated", Config("c.json", nil, []string{"mcpClients → mcp.clients"}), Warning},
		{"daemon mismatch", Daemon(true, true, "v1.0.0", "v1.1.0"), Warning},
		{"daemon running", Daemon(true, true, "v1.1.0", "v1.1.0"), OK},
		{"daemon stopped", Daemon(true, false, "", "v1.1.0"), OK},
		{"gateway off", GatewayPort(7821, true, "off: listen tcp: address already in use"), Warning},
		{"gateway clients none", GatewayClients(nil, nil), Warning},
		{"gateway clients", GatewayClients([]string{"~/.claude.json"}, nil), OK},
		{"keychain", Keychain(false), Warning},
		{"keychain not responding", KeychainNotResponding(), Warning},
	}
	for _, c := range cases {
		if c.got.Status != c.want || (c.want != OK && c.got.Fix == "") {
			t.Errorf("%s: %+v, want %s with a fix unless ok", c.name, c.got, c.want)
		}
	}
	if results := Organization("https://example.com/org.json", []string{"not read since"}); results[0].Status != Warning {
		t.Errorf("Organization = %+v, want a warning per notice", results)
	}
}

func TestGatewayPortProbesAStoppedDaemonsPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, isTCP := listener.Addr().(*net.TCPAddr)
	if !isTCP {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}
	port := addr.Port
	if r := GatewayPort(port, false, ""); r.Status != Warning {
		t.Fatalf("GatewayPort = %+v, want the port in use", r)
	}
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	if r := GatewayPort(port, false, ""); r.Status != OK {
		t.Fatalf("GatewayPort = %+v, want the port free", r)
	}
}
