package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeBinary(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".airlock-update-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestReplaceSwapsTheBinaryAndKeepsItsMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, BinaryName(runtime.GOOS))
	writeBinary(t, target, "old", 0o750)

	if err := Replace(target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target) // #nosec G304
	if err != nil || string(got) != "new" {
		t.Fatalf("target = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(target)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o750 {
			t.Fatalf("mode = %v, want 0750", info.Mode().Perm())
		}
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestReplaceRemovesTheTemporaryFileOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming a directory aside works on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "airlock")
	// A non-empty directory cannot be replaced by a file.
	if err := os.MkdirAll(filepath.Join(target, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Replace(target, []byte("new")); err == nil {
		t.Fatal("replacing a directory must fail")
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestReplaceInReadOnlyFolderSaysPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "airlock")
	writeBinary(t, target, "old", 0o755)
	if err := os.Chmod(dir, 0o500); err != nil { // #nosec G302
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302
			t.Error(err)
		}
	})

	err := Replace(target, []byte("new"))
	want := "cannot replace " + target + ": permission denied (" + permissionHint + ")"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if !strings.Contains(err.Error(), `sudo GH_TOKEN="$(gh auth token)" airlock update`) {
		t.Fatalf("the hint must carry the token through sudo: %v", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" { // #nosec G304
		t.Fatalf("target changed: %q, %v", got, readErr)
	}
}

func TestResolveTargetFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real-airlock")
	writeBinary(t, realPath, "old", 0o755)
	link := filepath.Join(dir, "airlock")
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}

	target, err := resolveTarget(link)
	if err != nil {
		t.Fatal(err)
	}
	wantReal, err := filepath.EvalSymlinks(realPath)
	if err != nil || target != wantReal {
		t.Fatalf("resolveTarget = %q, want %q", target, wantReal)
	}
	if err = Replace(target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, readErr := os.ReadFile(link); readErr != nil || string(got) != "new" { // #nosec G304
		t.Fatalf("through the symlink: %q, %v", got, readErr)
	}
	if info, lstatErr := os.Lstat(link); lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a file")
	}
}

func TestCleanupOldWithoutLeftover(t *testing.T) {
	if err := CleanupOld(filepath.Join(t.TempDir(), "airlock")); err != nil {
		t.Fatalf("CleanupOld = %v", err)
	}
}

func TestExecutableIsAbsolute(t *testing.T) {
	exe, err := Executable()
	if err != nil || !filepath.IsAbs(exe) || strings.TrimSpace(exe) == "" {
		t.Fatalf("Executable = %q, %v", exe, err)
	}
}
