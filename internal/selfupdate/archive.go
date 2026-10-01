package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// ChecksumsName is the release asset listing the SHA-256 of every archive.
const ChecksumsName = "checksums.txt"

// maxBinary caps the binary read from an archive; airlock is about 30 MB.
const maxBinary = 200 << 20

// BinaryName is the name of the airlock binary for goos.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "airlock.exe"
	}
	return "airlock"
}

// AssetName is the release archive of version for goos/goarch, as .goreleaser.yaml names it.
func AssetName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "airlock_" + version + "_" + goos + "_" + goarch + ext
}

// VerifyChecksum checks archive against the SHA-256 that checksums (a checksums.txt) lists for name.
func VerifyChecksum(archive, checksums []byte, name string) error {
	sum := sha256.Sum256(archive)
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name && strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
			return nil
		}
	}
	return fmt.Errorf("the download of %s does not match checksums.txt: nothing was changed", name)
}

// ExtractBinary returns the airlock binary of a release archive for goos: a zip on Windows, a
// tar.gz elsewhere.
func ExtractBinary(archive []byte, goos string) ([]byte, error) {
	if goos == "windows" {
		return fromZip(archive, BinaryName(goos))
	}
	return fromTarGz(archive, BinaryName(goos))
}

func fromTarGz(archive []byte, name string) (data []byte, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("cannot open the archive: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()

	tr := tar.NewReader(gz)
	for {
		header, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil, fmt.Errorf("the archive holds no %s", name)
		}
		if nextErr != nil {
			return nil, fmt.Errorf("cannot read the archive: %w", nextErr)
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == name {
			return readLimited(tr)
		}
	}
}

func fromZip(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("cannot open the archive: %w", err)
	}
	for _, file := range zr.File {
		if file.FileInfo().IsDir() || path.Base(file.Name) != name {
			continue
		}
		rc, openErr := file.Open()
		if openErr != nil {
			return nil, fmt.Errorf("cannot read the archive: %w", openErr)
		}
		data, readErr := readLimited(rc)
		if err := errors.Join(readErr, rc.Close()); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("the archive holds no %s", name)
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBinary+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read the archive: %w", err)
	}
	if len(data) > maxBinary {
		return nil, errors.New("the binary in the archive is too large")
	}
	return data, nil
}
