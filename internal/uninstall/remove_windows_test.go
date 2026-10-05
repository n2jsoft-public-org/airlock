//go:build windows

package uninstall

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"
)

func TestEncodeCommand(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(encodeCommand("Write-Host 'é'"))
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	if got := string(utf16.Decode(units)); got != "Write-Host 'é'" {
		t.Fatalf("decoded = %q", got)
	}
}

// TestDeleteAfterExitOf has the hidden PowerShell wait for a short-lived process, then checks it
// deleted the binary, its update leftovers and the emptied folder.
func TestDeleteAfterExitOf(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "airlock")
	exe := filepath.Join(dir, "airlock.exe")
	for _, path := range []string{exe, exe + ".old"} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("airlock"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	transcript := filepath.Join(t.TempDir(), "uninstall.log")
	t.Setenv("AIRLOCK_UNINSTALL_LOG", transcript)

	running := exec.Command("ping", "-n", "3", "127.0.0.1")
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	if err := deleteAfterExitOf(running.Process.Pid, exe, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("the binary went before its process exited: %v", err)
	}
	if err := running.Wait(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	log, err := os.ReadFile(transcript) // #nosec G304 -- a file of the test's temp dir.
	t.Fatalf("%s is still there after 30s; transcript (%v):\n%s", dir, err, log)
}

func TestOtherInstancesIgnoresThisProcess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := OtherInstances(exe); err != nil || n != 0 {
		t.Fatalf("OtherInstances(this test) = %d, %v; want 0", n, err)
	}
	if n, err := OtherInstances(filepath.Join(t.TempDir(), "airlock.exe")); err != nil || n != 0 {
		t.Fatalf("OtherInstances(not running) = %d, %v; want 0", n, err)
	}
	if n, err := OtherInstances(exe, os.Getpid()); err != nil || n != 0 {
		t.Fatalf("OtherInstances(this test, except itself) = %d, %v; want 0", n, err)
	}
}
