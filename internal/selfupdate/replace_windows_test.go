package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceMovesTheRunningBinaryAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "airlock.exe")
	writeBinary(t, target, "old", 0o755)

	if err := Replace(target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new" {
		t.Fatalf("target = %q, %v", got, err)
	}
	if got, err := os.ReadFile(target + ".old"); err != nil || string(got) != "old" {
		t.Fatalf(".old = %q, %v", got, err)
	}
	if err := CleanupOld(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatalf(".old still there: %v", err)
	}
}

func TestSwapRollsBackWhenTheNewBinaryCannotMoveIn(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "airlock.exe")
	writeBinary(t, target, "old", 0o755)

	if err := swap(filepath.Join(dir, "missing.exe"), target); err == nil {
		t.Fatal("swap of a missing file must fail")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old" {
		t.Fatalf("target after rollback = %q, %v", got, err)
	}
}
