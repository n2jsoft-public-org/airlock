package selfupdate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMarkerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "install.json")
	if _, err := ReadMarker(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadMarker of a missing file error = %v, want fs.ErrNotExist", err)
	}
	want := InstallMarker{Method: MethodScript, Path: "/home/dev/.local/bin/airlock"}
	if err := WriteMarker(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is from t.TempDir()
	if err != nil || string(data) != `{"method":"script","path":"/home/dev/.local/bin/airlock"}` {
		t.Fatalf("install.json = %s, %v", data, err)
	}
	if got, err := ReadMarker(path); err != nil || got != want {
		t.Fatalf("ReadMarker = %+v, %v, want %+v", got, err, want)
	}
}

func TestMarkerDesignatesASymlinkedExecutable(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "airlock-real")
	writeBinary(t, binary, "airlock", 0o755)
	link := filepath.Join(dir, "airlock")
	if err := os.Symlink(binary, link); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
	other := filepath.Join(dir, "other")
	writeBinary(t, other, "other", 0o755)

	for _, tt := range []struct {
		path, exe string
		want      bool
	}{{link, binary, true}, {binary, link, true}, {link, link, true}, {other, link, false}, {"", link, false}} {
		if got := (InstallMarker{Method: MethodScript, Path: tt.path}).designates(tt.exe, runtime.GOOS); got != tt.want {
			t.Errorf("marker %q designates %q = %v, want %v", tt.path, tt.exe, got, tt.want)
		}
	}
}

func TestMarkerPathsAreCaseInsensitiveOnWindowsOnly(t *testing.T) {
	m := InstallMarker{Method: MethodScript, Path: filepath.FromSlash("/missing/Airlock/airlock.exe")}
	exe := filepath.FromSlash("/missing/airlock/AIRLOCK.exe")
	if !m.designates(exe, "windows") {
		t.Error("on Windows the paths must match whatever their case")
	}
	if m.designates(exe, "linux") {
		t.Error("on Linux paths of another case are other files")
	}
}

func TestMarkerDesignatesTheRunningBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !(InstallMarker{Method: MethodScript, Path: exe}).DesignatesRunning() {
		t.Fatal("a marker of os.Executable must designate the running binary")
	}
	if (InstallMarker{Method: MethodScript, Path: filepath.Join(t.TempDir(), "airlock")}).DesignatesRunning() {
		t.Fatal("a marker of another binary must not designate the running one")
	}
}
