// Package appdir resolves where airlock keeps its user files (config, logs and preferences).
package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	appName = "airlock"
	// legacyAppName is the directory used before airlock was extracted from acme-cli.
	legacyAppName = "jumphost"
)

// ConfigDir is $XDG_CONFIG_HOME/airlock, defaulting to ~/.config/airlock
// (%AppData%\airlock on Windows). The directory is not created.
func ConfigDir() (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, appName), nil
}

// configRoot is the directory holding per-application config directories.
func configRoot() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return xdg, nil
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve the user config directory: %w", err)
		}
		return base, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve the home directory: %w", err)
	}
	return filepath.Join(home, ".config"), nil
}

// ConfigFile is the settings file under ConfigDir.
func ConfigFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// SavedConnectionsFile is the file under ConfigDir holding every saved connection: the direct
// databases and the targets reached through a jumphost.
func SavedConnectionsFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "saved-connections.json"), nil
}

// ConnectionsFile is the connections.json under ConfigDir of an earlier airlock, moved into
// SavedConnectionsFile once.
func ConnectionsFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "connections.json"), nil
}

// DatabasesFile is the databases.json under ConfigDir of an earlier airlock, moved into
// SavedConnectionsFile once.
func DatabasesFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "databases.json"), nil
}

// KubeconfigDir is the directory under ConfigDir holding the kubeconfig of each EKS session. The
// directory is not created.
func KubeconfigDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kube"), nil
}

// LogsDir is the logs directory under ConfigDir. The directory is not created.
func LogsDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

// UpdateCheckFile is the file under ConfigDir keeping the last check of the latest airlock release.
func UpdateCheckFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-check.json"), nil
}

// InstallMarkerFile is the file under ConfigDir telling how airlock was installed and where
// (selfupdate.InstallMarker).
func InstallMarkerFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "install.json"), nil
}

// OrganizationCacheFile is the file under ConfigDir keeping the last organization config fetched
// from a remote source.
func OrganizationCacheFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "organization-cache.json"), nil
}

// ManagedFile is where an administrator drops the managed config for goos, or "" when goos has
// none. It is read-only for airlock.
func ManagedFile(goos string) string {
	switch goos {
	case "darwin":
		return "/Library/Application Support/airlock/managed.json"
	case "linux":
		return "/etc/airlock/managed.json"
	case "windows":
		root := os.Getenv("ProgramData")
		if root == "" {
			root = `C:\ProgramData`
		}
		return root + `\airlock\managed.json`
	}
	return ""
}
