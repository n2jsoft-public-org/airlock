package mcpserve

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/n2jsoft/airlock/internal/aiclient"
	"github.com/n2jsoft/airlock/internal/platform"
)

// ClientEntry is the MCP server entry written into AI client configuration files.
type ClientEntry struct {
	Name  string
	URL   string
	Token string
}

// configFormat adds and removes a server entry in one kind of client configuration file.
type configFormat interface {
	add(path string, entry ClientEntry) error
	// remove deletes entry unless another session replaced it (its URL differs); removed is
	// false when the file is left unchanged.
	remove(path string, entry ClientEntry) (removed bool, err error)
	// has reports whether path holds entry: its name with its URL.
	has(path string, entry ClientEntry) (bool, error)
}

// clientSpec is where and how one AI client reads its MCP servers.
type clientSpec struct {
	// detect reports whether the project uses the client, for auto-detection.
	detect func(projectDir string) bool
	// userPath returns the client's user-level configuration file.
	userPath func() (string, error)
	format   configFormat
	// companion is a user-level file of another app of the same vendor, written along userPath.
	companion *companionFile
	// projectPath is the client's configuration file, relative to the project directory.
	projectPath string
}

// companionFile is the user-level file of an app sharing a client's servers, written only when the
// app is installed (its configuration directory exists).
type companionFile struct {
	format configFormat
	// dir returns the app's configuration directory.
	dir func() (string, error)
	// name is the file in dir.
	name string
}

var clientSpecs = map[aiclient.Client]clientSpec{
	aiclient.Claude: {
		projectPath: ".mcp.json",
		// Only an existing .mcp.json: .claude/ also holds settings, worktrees and so on, and a project
		// using Claude Code does not necessarily want shared project servers.
		detect: func(dir string) bool { return exists(filepath.Join(dir, ".mcp.json")) },
		// Claude Code keeps its user-level servers in .claude.json, in CLAUDE_CONFIG_DIR or the home.
		userPath: func() (string, error) {
			if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
				return filepath.Join(dir, ".claude.json"), nil
			}
			return homePath(".claude.json")
		},
		format: jsonFormat{key: "mcpServers", entry: httpEntry},
		// Claude Desktop keeps its servers in claude_desktop_config.json, in its directory of the
		// user configuration (Application Support on macOS, AppData on Windows).
		companion: &companionFile{
			dir: func() (string, error) {
				dir, err := userConfigDir()
				if err != nil {
					return "", err
				}
				return filepath.Join(dir, "Claude"), nil
			},
			name:   "claude_desktop_config.json",
			format: jsonFormat{key: "mcpServers", entry: proxyEntry},
		},
	},
	aiclient.VSCode: {
		projectPath: filepath.Join(".vscode", "mcp.json"),
		detect:      dirDetector(".vscode"),
		userPath: func() (string, error) {
			dir, err := userConfigDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(dir, "Code", "User", "mcp.json"), nil
		},
		format: jsonFormat{key: "servers", entry: httpEntry},
	},
	aiclient.Codex: {
		projectPath: filepath.Join(".codex", "config.toml"),
		detect:      dirDetector(".codex"),
		userPath: func() (string, error) {
			if dir := getenv("CODEX_HOME"); dir != "" {
				return filepath.Join(dir, "config.toml"), nil
			}
			return homePath(".codex", "config.toml")
		},
		format: codexFormat{},
	},
	aiclient.Cursor: {
		projectPath: filepath.Join(".cursor", "mcp.json"),
		detect:      dirDetector(".cursor"),
		userPath:    func() (string, error) { return homePath(".cursor", "mcp.json") },
		format:      jsonFormat{key: "mcpServers", entry: cursorEntry},
	},
	aiclient.Gemini: {
		projectPath: filepath.Join(".gemini", "settings.json"),
		detect:      dirDetector(".gemini"),
		userPath:    func() (string, error) { return homePath(".gemini", "settings.json") },
		format:      jsonFormat{key: "mcpServers", entry: geminiEntry},
	},
}

func dirDetector(name string) func(string) bool {
	return func(dir string) bool { return exists(filepath.Join(dir, name)) }
}

// lockRoot returns the directory holding the config file locks; overridden in tests.
var lockRoot = func() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "airlock", "mcp-config"), nil
}

// userHomeDir, userConfigDir and getenv locate the user-level client files, and executable the
// airlock binary the stdio clients launch; overridden in tests.
var (
	userHomeDir   = os.UserHomeDir
	userConfigDir = os.UserConfigDir
	getenv        = os.Getenv
	executable    = os.Executable
)

func homePath(elem ...string) (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, elem...)...), nil
}

// isHomeDir reports whether projectDir is the user's home directory. Project files written there
// would usually go unnoticed (those directories tend to exist already for unrelated reasons) and
// some are the user-level files themselves (~/.codex/config.toml), so they are left to the user scope.
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

// clientFile is one configuration file to write a server entry into.
type clientFile struct {
	format configFormat
	path   string
}

// clientFiles resolves clients into files. detected tells whether auto-detection keeps the project
// file of a client. A user-level file that cannot be located is an error; the others still apply.
func clientFiles(projectDir string, clients aiclient.Selection, detected func(*clientSpec) bool) ([]clientFile, error) {
	var targets []aiclient.Target
	if clients.Detect {
		for _, client := range aiclient.Clients {
			if detected(new(clientSpecs[client])) {
				targets = append(targets, aiclient.Target{Client: client, Scope: aiclient.ScopeProject})
			}
		}
	}
	targets = append(targets, clients.Targets...)

	inHome := isHomeDir(projectDir)
	seen := map[string]bool{}
	var files []clientFile
	var errs []error
	add := func(file clientFile) {
		if !seen[file.path] {
			seen[file.path] = true
			files = append(files, file)
		}
	}
	for _, target := range targets {
		spec, ok := clientSpecs[target.Client]
		if !ok {
			errs = append(errs, fmt.Errorf("unknown AI client %q", target.Client))
			continue
		}
		switch target.Scope {
		case aiclient.ScopeUser:
			userPath, err := spec.userPath()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: failed to locate the user configuration: %w", target, err))
				continue
			}
			add(clientFile{format: spec.format, path: userPath})
			if spec.companion == nil {
				continue
			}
			companion, installed, err := spec.companion.file()
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("%s: failed to locate the %s: %w", target, spec.companion.name, err))
			case installed:
				add(companion)
			}
		default:
			if inHome {
				continue
			}
			add(clientFile{format: spec.format, path: filepath.Join(projectDir, spec.projectPath)})
		}
	}
	return files, errors.Join(errs...)
}

// file returns the companion's file; installed is false when the app is not installed.
func (c *companionFile) file() (file clientFile, installed bool, err error) {
	dir, err := c.dir()
	if err != nil {
		return clientFile{}, false, err
	}
	return clientFile{format: c.format, path: filepath.Join(dir, c.name)}, exists(dir), nil
}

// RegisterClients adds entry to the MCP configuration of the selected AI clients, and returns the
// files it wrote.
func RegisterClients(projectDir string, clients aiclient.Selection, entry ClientEntry) ([]string, error) {
	files, err := clientFiles(projectDir, clients, func(spec *clientSpec) bool { return spec.detect(projectDir) })
	errs := []error{err}
	var written []string
	for _, file := range files {
		if err := file.format.add(file.path, entry); err != nil {
			errs = append(errs, err)
			continue
		}
		written = append(written, file.path)
	}
	return written, errors.Join(errs...)
}

// UnregisterClients removes entry from the files of the selected clients, unless another session
// replaced it (its URL differs), and returns the files it changed. Auto-detection covers every
// client: the project may have stopped looking like one since the entry was written.
func UnregisterClients(projectDir string, clients aiclient.Selection, entry ClientEntry) ([]string, error) {
	files, err := clientFiles(projectDir, clients, func(*clientSpec) bool { return true })
	errs := []error{err}
	var changed []string
	for _, file := range files {
		if !exists(file.path) {
			continue
		}
		removed, err := file.format.remove(file.path, entry)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if removed {
			changed = append(changed, file.path)
		}
	}
	return changed, errors.Join(errs...)
}

// EntryFiles returns the files of the selected clients holding entry (its name with its URL).
// Project files are looked up in no project: pass user-level targets.
func EntryFiles(clients aiclient.Selection, entry ClientEntry) ([]string, error) {
	files, err := clientFiles("", clients, func(*clientSpec) bool { return false })
	errs := []error{err}
	var found []string
	for _, file := range files {
		if !exists(file.path) {
			continue
		}
		ok, hasErr := file.format.has(file.path, entry)
		if hasErr != nil {
			errs = append(errs, hasErr)
			continue
		}
		if ok {
			found = append(found, file.path)
		}
	}
	return found, errors.Join(errs...)
}

// jsonFormat is a JSON file holding the servers in the object under key.
type jsonFormat struct {
	// entry is the value written for a server.
	entry func(ClientEntry) (any, error)
	key   string
}

// httpEntry is the server entry of Claude Code and VS Code.
func httpEntry(e ClientEntry) (any, error) {
	return struct {
		Headers map[string]string `json:"headers"`
		Type    string            `json:"type"`
		URL     string            `json:"url"`
	}{Headers: bearerHeader(e.Token), Type: "http", URL: e.URL}, nil
}

// cursorEntry is the server entry of Cursor, which tells HTTP servers by their url.
func cursorEntry(e ClientEntry) (any, error) {
	return struct {
		Headers map[string]string `json:"headers"`
		URL     string            `json:"url"`
	}{Headers: bearerHeader(e.Token), URL: e.URL}, nil
}

// geminiEntry is the server entry of Gemini CLI, whose httpUrl selects the streamable HTTP transport.
func geminiEntry(e ClientEntry) (any, error) {
	return struct {
		Headers map[string]string `json:"headers"`
		HTTPURL string            `json:"httpUrl"`
	}{Headers: bearerHeader(e.Token), HTTPURL: e.URL}, nil
}

// proxyEntry is the server entry of clients limited to stdio servers (Claude Desktop): airlock
// mcp-proxy relays the server, with the token in its environment.
func proxyEntry(e ClientEntry) (any, error) {
	airlock, err := executable()
	if err != nil {
		return nil, fmt.Errorf("failed to locate the airlock binary: %w", err)
	}
	return struct {
		Env     map[string]string `json:"env"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
	}{Command: airlock, Args: []string{ProxyCommand, e.URL}, Env: map[string]string{ProxyTokenEnv: e.Token}}, nil
}

func bearerHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func (f jsonFormat) add(path string, entry ClientEntry) error {
	server, err := f.entry(entry)
	if err != nil {
		return err
	}
	value, err := marshalNoEscape(server)
	if err != nil {
		return err
	}
	return updateConfig(path, f.key, func(servers *orderedObject) bool {
		if current, ok := servers.get(entry.Name); ok && sameJSON(current, value) {
			// Unchanged: the file (which its client may rewrite meanwhile) is left alone.
			return false
		}
		servers.set(entry.Name, value)
		return true
	})
}

// sameJSON reports whether a and b encode the same JSON, whitespace aside.
func sameJSON(a, b []byte) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

// entryHasURL reports whether the server entry raw points at url: in url, httpUrl, or the arguments
// of airlock mcp-proxy.
func entryHasURL(raw []byte, url string) bool {
	var current struct {
		URL     string   `json:"url"`
		HTTPURL string   `json:"httpUrl"`
		Args    []string `json:"args"`
	}
	if json.Unmarshal(raw, &current) != nil {
		return false
	}
	return current.URL == url || current.HTTPURL == url || slices.Contains(current.Args, url)
}

func (f jsonFormat) has(path string, entry ClientEntry) (bool, error) {
	found := false
	err := updateConfig(path, f.key, func(servers *orderedObject) bool {
		raw, ok := servers.get(entry.Name)
		if !ok {
			return false
		}
		found = entryHasURL(raw, entry.URL)
		return false
	})
	return found, err
}

func (f jsonFormat) remove(path string, entry ClientEntry) (bool, error) {
	removed := false
	err := updateConfig(path, f.key, func(servers *orderedObject) bool {
		raw, ok := servers.get(entry.Name)
		if !ok {
			return false
		}
		if !entryHasURL(raw, entry.URL) {
			return false
		}
		servers.remove(entry.Name)
		removed = true
		return true
	})
	return removed, err
}

// updateConfig applies change to the servers object of a config file under a cross-process lock.
// change returns false when nothing needs to be written.
func updateConfig(path, key string, change func(servers *orderedObject) bool) (err error) {
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()

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
	encodedServers, err := marshalNoEscape(&servers)
	if err != nil {
		return err
	}
	root.set(key, encodedServers)

	output, err := marshalIndentNoEscape(&root, indent)
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
		return errors.Join(err, os.Remove(tmp.Name()))
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
