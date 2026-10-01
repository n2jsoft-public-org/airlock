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

func TestCleanupOldRemovesEveryLeftover(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "airlock.exe")
	writeBinary(t, target, "new", 0o755)
	writeBinary(t, target+".old", "old", 0o755)
	writeBinary(t, target+".old-abc", "older", 0o755)
	other := filepath.Join(dir, "other.exe.old")
	writeBinary(t, other, "unrelated", 0o755)

	if err := CleanupOld(target); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{target + ".old", target + ".old-abc"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s still there: %v", gone, err)
		}
	}
	for _, kept := range []string{target, other} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s must stay: %v", kept, err)
		}
	}
}

func TestSwapWorksWhenAnOldBinaryAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "airlock.exe")
	writeBinary(t, target, "second", 0o755)
	writeBinary(t, target+".old", "first", 0o755)
	newPath := filepath.Join(dir, "new.exe")
	writeBinary(t, newPath, "third", 0o755)

	if err := swap(newPath, target); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "third" {
		t.Fatalf("target = %q, %v", got, err)
	}
}
