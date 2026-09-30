package dbmcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/n2jsoft/airlock/internal/platform"
)

// ClientEntry is the MCP server entry written into AI client configuration files.
type ClientEntry struct {
	Name  string
	URL   string
	Token string
}

// clientFile is one AI client's project-level MCP configuration file.
type clientFile struct {
	// enabled reports whether the project uses this client, so the file may be written.
	enabled func(projectDir string) bool
	path    string
	key     string
}

var clientFiles = []clientFile{
	{ // Claude Code
		path: ".mcp.json",
		key:  "mcpServers",
		enabled: func(dir string) bool {
			return exists(filepath.Join(dir, ".mcp.json")) || exists(filepath.Join(dir, ".claude"))
		},
	},
	{ // VS Code
		path:    filepath.Join(".vscode", "mcp.json"),
		key:     "servers",
		enabled: func(dir string) bool { return exists(filepath.Join(dir, ".vscode")) },
	},
}

// lockRoot returns the directory holding the config file locks; overridden in tests.
var lockRoot = func() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "airlock", "mcp-config"), nil
}

// userHomeDir returns the user's home directory; overridden in tests.
var userHomeDir = os.UserHomeDir

// isHomeDir reports whether projectDir is the user's home directory. Writing .mcp.json or
// .vscode/mcp.json there would usually go unnoticed (those directories tend to exist already
// for unrelated reasons) and is not read by any client, which only looks at the project directory.
func isHomeDir(projectDir string) bool {
	home, err := userHomeDir()
	if err != nil {
		return false
	}
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		return false
	}
	return filepath.Clean(absProject) == filepath.Clean(home)
}

type serverEntry struct {
	Headers map[string]string `json:"headers"`
	Type    string            `json:"type"`
	URL     string            `json:"url"`
}

// RegisterClients adds entry to the MCP configuration of every AI client the project uses,
// and returns the files it wrote.
func RegisterClients(projectDir string, entry ClientEntry) ([]string, error) {
	if isHomeDir(projectDir) {
		return nil, nil
	}
	value, err := json.Marshal(serverEntry{
		Type:    "http",
		URL:     entry.URL,
		Headers: map[string]string{"Authorization": "Bearer " + entry.Token},
	})
	if err != nil {
		return nil, err
	}

	var written []string
	var errs []error
	for _, file := range clientFiles {
		if !file.enabled(projectDir) {
			continue
		}
		path := filepath.Join(projectDir, file.path)
		err := updateConfig(path, file.key, func(servers *orderedObject) bool {
			servers.set(entry.Name, value)
			return true
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		written = append(written, path)
	}
	return written, errors.Join(errs...)
}

// UnregisterClients removes entry from the client files, unless another session replaced it
// (its URL differs), and returns the files it changed.
func UnregisterClients(projectDir string, entry ClientEntry) ([]string, error) {
	var changed []string
	var errs []error
	for _, file := range clientFiles {
		path := filepath.Join(projectDir, file.path)
		if !exists(path) {
			continue
		}
		removed := false
		err := updateConfig(path, file.key, func(servers *orderedObject) bool {
			raw, ok := servers.get(entry.Name)
			if !ok {
				return false
			}
			var current serverEntry
			if json.Unmarshal(raw, &current) != nil || current.URL != entry.URL {
				return false
			}
			servers.remove(entry.Name)
			removed = true
			return true
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if removed {
			changed = append(changed, path)
		}
	}
	return changed, errors.Join(errs...)
}

// updateConfig applies change to the servers object of a config file under a cross-process lock.
// change returns false when nothing needs to be written.
func updateConfig(path, key string, change func(servers *orderedObject) bool) error {
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	root := orderedObject{}
	indent := defaultIndent
	payload, err := os.ReadFile(path) // #nosec G304 -- path is a known client file inside the project.
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("failed to read %s: %w", path, err)
	default:
		if parseErr := json.Unmarshal(payload, &root); parseErr != nil {
			return fmt.Errorf("%s is not plain JSON (comments are not supported): %w", path, parseErr)
		}
		indent = detectIndent(payload)
	}

	servers := orderedObject{}
	if raw, ok := root.get(key); ok {
		if parseErr := json.Unmarshal(raw, &servers); parseErr != nil {
			return fmt.Errorf("%s: %q is not an object: %w", path, key, parseErr)
		}
	}
	if !change(&servers) {
		return nil
	}
	encodedServers, err := marshalNoEscape(servers)
	if err != nil {
		return err
	}
	root.set(key, encodedServers)

	output, err := marshalIndentNoEscape(root, indent)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, output)
}

// defaultIndent is used for new files and files without indented lines.
const defaultIndent = "  "

// detectIndent returns the indentation of the first indented line, so rewriting keeps the file's style.
func detectIndent(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return defaultIndent
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	// A new file gets restrictive permissions (it may end up holding a bearer token); an existing
	// file keeps whatever mode it already had.
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr, os.Chmod(tmp.Name(), mode)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// lockConfig takes an exclusive cross-process lock for path, kept outside the project.
func lockConfig(path string) (unlock func() error, err error) {
	root, err := lockRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve lock directory: %w", err)
	}
	if mkdirErr := os.MkdirAll(root, 0o700); mkdirErr != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", mkdirErr)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(absolute))
	file, err := os.OpenFile(filepath.Join(root, hex.EncodeToString(sum[:12])+".lock"), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- lock path is derived from a hash inside our cache directory.
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}
	if err := platform.LockFile(file); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to lock %s: %w", file.Name(), err), file.Close())
	}
	return func() error {
		return errors.Join(platform.UnlockFile(file), file.Close())
	}, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
