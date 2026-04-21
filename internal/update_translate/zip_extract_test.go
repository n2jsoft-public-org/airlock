package updatetranslate

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractZip_OverwritesExistingFileWhenEnabled(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "bundle.zip")
	destination := filepath.Join(tempDir, "out")

	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	existingPath := filepath.Join(destination, "messages", "en.json")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o750); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(existingPath, []byte("old"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	entries := map[string]string{
		"messages/en.json": "new-content",
		"messages/fr.json": "bonjour",
	}
	createZipFile(t, zipPath, entries)

	if err := extractZip(zipPath, destination, true); err != nil {
		t.Fatalf("extract zip: %v", err)
	}

	// #nosec G304 -- test uses controlled temp file path.
	content, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("read overwritten file: %v", err)
	}
	if string(content) != "new-content" {
		t.Fatalf("unexpected overwritten content: %s", string(content))
	}
}

func TestExtractZip_RefusesOverwriteWhenDisabled(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "bundle.zip")
	destination := filepath.Join(tempDir, "out")

	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	existingPath := filepath.Join(destination, "messages", "en.json")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o750); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(existingPath, []byte("old"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	entries := map[string]string{"messages/en.json": "new-content"}
	createZipFile(t, zipPath, entries)

	err := extractZip(zipPath, destination, false)
	if err == nil {
		t.Fatal("expected overwrite error, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractZip_RejectsZipSlipEntry(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "bundle.zip")
	destination := filepath.Join(tempDir, "out")

	entries := map[string]string{"../evil.txt": "bad"}
	createZipFile(t, zipPath, entries)

	err := extractZip(zipPath, destination, true)
	if err == nil {
		t.Fatal("expected zip-slip error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func createZipFile(t *testing.T, zipPath string, entries map[string]string) {
	t.Helper()

	// #nosec G304 -- test uses controlled temp file path.
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip file: %v", err)
	}

	writer := zip.NewWriter(f)
	for name, content := range entries {
		w, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
}
