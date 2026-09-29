// Package appdir resolves where the CLI keeps its user files (logs, and preferences later on).
package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const appName = "jumphost"

// ConfigDir is $XDG_CONFIG_HOME/jumphost, defaulting to ~/.config/jumphost
// (%AppData%\jumphost on Windows). The directory is not created.
func ConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, appName), nil
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve the user config directory: %w", err)
		}
		return filepath.Join(base, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve the home directory: %w", err)
	}
	return filepath.Join(home, ".config", appName), nil
}

// LogsDir is the logs directory under ConfigDir. The directory is not created.
func LogsDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}
