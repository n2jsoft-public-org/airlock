package updatetranslate

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxZipEntryBytes uint64 = 32 * 1024 * 1024

func extractZip(zipPath, destination string, overwrite bool) (returnErr error) {
	archive, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer func() {
		if closeErr := archive.Close(); closeErr != nil && returnErr == nil {
			returnErr = fmt.Errorf("close zip archive: %w", closeErr)
		}
	}()

	for _, file := range archive.File {
		if file.UncompressedSize64 > maxZipEntryBytes {
			return fmt.Errorf("archive entry %s exceeds max size of %d bytes", file.Name, maxZipEntryBytes)
		}

		targetPath, err := zipEntryDestination(destination, file.Name)
		if err != nil {
			return err
		}

		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("unsupported symlink entry in archive: %s", file.Name)
		}

		if file.FileInfo().IsDir() {
			if mkdirErr := os.MkdirAll(targetPath, 0o750); mkdirErr != nil {
				return fmt.Errorf("create directory %s: %w", targetPath, mkdirErr)
			}
			continue
		}

		if mkdirErr := os.MkdirAll(filepath.Dir(targetPath), 0o750); mkdirErr != nil {
			return fmt.Errorf("create parent directory for %s: %w", targetPath, mkdirErr)
		}

		if !overwrite {
			if _, statErr := os.Stat(targetPath); statErr == nil {
				return fmt.Errorf("destination file already exists: %s", targetPath)
			} else if !os.IsNotExist(statErr) {
				return fmt.Errorf("check destination file %s: %w", targetPath, statErr)
			}
		}

		in, err := file.Open()
		if err != nil {
			return fmt.Errorf("open archived file %s: %w", file.Name, err)
		}

		// #nosec G304 -- targetPath is validated to stay within destination.
		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
		if err != nil {
			if closeErr := in.Close(); closeErr != nil {
				return fmt.Errorf("close archived file %s: %w", file.Name, closeErr)
			}
			return fmt.Errorf("create destination file %s: %w", targetPath, err)
		}

		_, copyErr := io.CopyN(out, in, int64(file.UncompressedSize64))
		closeOutErr := out.Close()
		closeInErr := in.Close()

		if copyErr != nil {
			return fmt.Errorf("extract file %s: %w", file.Name, copyErr)
		}
		if closeOutErr != nil {
			return fmt.Errorf("close destination file %s: %w", targetPath, closeOutErr)
		}
		if closeInErr != nil {
			return fmt.Errorf("close archived file %s: %w", file.Name, closeInErr)
		}
	}

	return nil
}

func zipEntryDestination(destination, archivePath string) (string, error) {
	cleanDestination := filepath.Clean(destination)
	cleanArchivePath := filepath.Clean(archivePath)
	if cleanArchivePath == "." {
		return "", fmt.Errorf("invalid archive entry: %q", archivePath)
	}

	targetPath := filepath.Join(cleanDestination, cleanArchivePath)
	cleanTarget := filepath.Clean(targetPath)
	prefix := cleanDestination + string(os.PathSeparator)

	if cleanTarget != cleanDestination && !strings.HasPrefix(cleanTarget, prefix) {
		return "", fmt.Errorf("archive entry %q escapes destination", archivePath)
	}

	return cleanTarget, nil
}
