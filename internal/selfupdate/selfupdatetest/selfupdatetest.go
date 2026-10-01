// Package selfupdatetest fakes the GitHub releases API and builds release archives for the tests
// of airlock update.
package selfupdatetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// AssetName is the archive name .goreleaser.yaml gives the build of tag for goos/goarch.
func AssetName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "airlock_" + tag + "_" + goos + "_" + goarch + ext
}

// Archive returns an archive like the ones .goreleaser.yaml builds for goos: a tar.gz (a zip on
// Windows) holding README.md and the airlock binary, whose content is binary.
func Archive(t *testing.T, goos string, binary []byte) []byte {
	t.Helper()
	name := "airlock"
	if goos == "windows" {
		name = "airlock.exe"
	}
	files := []struct {
		name string
		data []byte
	}{{"README.md", []byte("# airlock\n")}, {name, binary}}

	var buf bytes.Buffer
	if goos == "windows" {
		zw := zip.NewWriter(&buf)
		for _, f := range files {
			w, err := zw.Create(f.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(f.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Release is the latest release a fake GitHub serves.
type Release struct {
	// Assets maps each asset name to its content.
	Assets map[string][]byte
	Tag    string
	// Compare is the status answered to any commit comparison; "" answers 404.
	Compare string
}

// NewRelease returns the release tag with the archive of binary for goos/goarch and its
// checksums.txt.
func NewRelease(t *testing.T, tag, goos, goarch string, binary []byte) Release {
	t.Helper()
	archive := Archive(t, goos, binary)
	name := AssetName(tag, goos, goarch)
	sum := sha256.Sum256(archive)
	return Release{Tag: tag, Assets: map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"),
	}}
}
