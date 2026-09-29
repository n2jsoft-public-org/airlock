package appdir

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// MigrateLegacy copies connections.json and logs/ from the former jumphost config directory into
// airlock's, once: only while airlock's directory does not exist yet. The old directory is left
// untouched. It reports whether anything was copied.
func MigrateLegacy() (bool, error) {
	root, err := configRoot()
	if err != nil {
		return false, err
	}
	return migrateFrom(filepath.Join(root, legacyAppName), filepath.Join(root, appName))
}

func migrateFrom(legacyDir, dir string) (bool, error) {
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return false, nil // already there (or unreadable): never merge into it.
	}
	if info, err := os.Stat(legacyDir); err != nil || !info.IsDir() {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("failed to create %s: %w", dir, err)
	}

	var errs []error
	if err := copyFile(filepath.Join(legacyDir, "connections.json"), filepath.Join(dir, "connections.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	logs, err := os.ReadDir(filepath.Join(legacyDir, "logs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if len(logs) > 0 {
		if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
			errs = append(errs, err)
		} else {
			for _, entry := range logs {
				if !entry.Type().IsRegular() {
					continue
				}
				if err := copyFile(filepath.Join(legacyDir, "logs", entry.Name()), filepath.Join(dir, "logs", entry.Name())); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return true, fmt.Errorf("partial migration from %s: %w", legacyDir, err)
	}
	return true, nil
}

// copyFile copies src to a new dst readable by the user only; dst must not exist.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) // #nosec G304 -- src is inside the user's own config directory.
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- dst is inside airlock's config directory.
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}
