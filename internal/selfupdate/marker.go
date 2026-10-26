package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MethodScript is the install method of an airlock installed by an install script or airlock update.
const MethodScript = "script"

// InstallMarker is install.json in airlock's config directory (appdir.InstallMarkerFile): how airlock
// was installed and where. The install scripts write the same fields: keep them.
type InstallMarker struct {
	Method string `json:"method"`
	// Path is the absolute path of the airlock binary.
	Path string `json:"path"`
}

// ReadMarker reads the marker at path; a missing file gives an error matching fs.ErrNotExist.
func ReadMarker(path string) (InstallMarker, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is airlock's own marker file.
	if err != nil {
		return InstallMarker{}, err
	}
	var m InstallMarker
	if err = json.Unmarshal(data, &m); err != nil {
		return InstallMarker{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	return m, nil
}

// WriteMarker writes m to path.
func WriteMarker(path string, m InstallMarker) error {
	return writeJSON(path, m)
}

// ScriptInstalled tells whether the marker at markerPath says an install script installed the
// airlock binary at exe: only then may airlock update and airlock uninstall replace or remove it.
func ScriptInstalled(markerPath, exe string) bool {
	m, err := ReadMarker(markerPath)
	return err == nil && m.Method == MethodScript && m.designates(exe, runtime.GOOS)
}

// designates tells whether m.Path is exe, symlinks resolved; paths are case-insensitive on Windows.
func (m InstallMarker) designates(exe, goos string) bool {
	return m.Path != "" && samePath(m.Path, exe, goos)
}

// samePath tells whether a and b are the same file, symlinks resolved; case-insensitive on Windows.
func samePath(a, b, goos string) bool {
	a, b = resolvedPath(a), resolvedPath(b)
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// resolvedPath is path with its symlinks resolved, or only cleaned when it cannot be resolved.
func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
