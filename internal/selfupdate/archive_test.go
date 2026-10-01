package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/selfupdate/selfupdatetest"
)

func TestAssetName(t *testing.T) {
	if got := AssetName("20261005-3f9a1c2", "darwin", "arm64"); got != "airlock_20261005-3f9a1c2_darwin_arm64.tar.gz" {
		t.Errorf("darwin asset = %q", got)
	}
	if got := AssetName("20261005-3f9a1c2", "windows", "amd64"); got != "airlock_20261005-3f9a1c2_windows_amd64.zip" {
		t.Errorf("windows asset = %q", got)
	}
	if got := selfupdatetest.AssetName("20261005-3f9a1c2", "linux", "amd64"); got != AssetName("20261005-3f9a1c2", "linux", "amd64") {
		t.Errorf("the test helper names assets %q", got)
	}
}

func TestExtractBinary(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		archive := selfupdatetest.Archive(t, goos, []byte("new airlock"))
		got, err := ExtractBinary(archive, goos)
		if err != nil || string(got) != "new airlock" {
			t.Errorf("%s: ExtractBinary = %q, %v", goos, got, err)
		}
	}
}

func TestExtractBinaryWithoutTheBinary(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	readme := []byte("# airlock\n")
	if err := tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: int64(len(readme)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(readme); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractBinary(buf.Bytes(), "linux")
	if err == nil || err.Error() != "the archive holds no airlock" {
		t.Fatalf("error = %v, want the missing binary", err)
	}
}

func TestExtractBinaryOfTheWrongFormat(t *testing.T) {
	if _, err := ExtractBinary(selfupdatetest.Archive(t, "linux", []byte("x")), "windows"); err == nil {
		t.Fatal("a tar.gz read as a zip must fail")
	}
	if _, err := ExtractBinary(selfupdatetest.Archive(t, "windows", []byte("x")), "linux"); err == nil {
		t.Fatal("a zip read as a tar.gz must fail")
	}
}

func checksums(archive []byte, name string) []byte {
	sum := sha256.Sum256(archive)
	return []byte("0000  other.tar.gz\n" + hex.EncodeToString(sum[:]) + "  " + name + "\n")
}

func TestVerifyChecksum(t *testing.T) {
	archive := []byte("archive bytes")
	name := "airlock_20261005-3f9a1c2_linux_amd64.tar.gz"
	if err := VerifyChecksum(archive, checksums(archive, name), name); err != nil {
		t.Fatalf("VerifyChecksum = %v", err)
	}

	want := "the download of " + name + " does not match checksums.txt: nothing was changed"
	for label, sums := range map[string][]byte{
		"mismatch":      checksums([]byte("other bytes"), name),
		"missing entry": checksums(archive, "airlock_other.tar.gz"),
		"empty":         nil,
	} {
		err := VerifyChecksum(archive, sums, name)
		if err == nil || err.Error() != want {
			t.Errorf("%s: VerifyChecksum error = %v, want %q", label, err, want)
		}
	}
}

func TestVerifyChecksumIgnoresCase(t *testing.T) {
	archive := []byte("archive bytes")
	sum := sha256.Sum256(archive)
	sums := []byte(strings.ToUpper(hex.EncodeToString(sum[:])) + "  a.tar.gz\n")
	if err := VerifyChecksum(archive, sums, "a.tar.gz"); err != nil {
		t.Fatalf("an upper-case checksum must match: %v", err)
	}
}
