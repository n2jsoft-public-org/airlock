// Package agentskill writes the airlock agent skill (SKILL.md) where the AI clients load their
// skills: .claude/skills for Claude Code, .agents/skills (the shared Agent Skills location) for
// VS Code, Codex, Cursor and Gemini CLI.
package agentskill

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/n2jsoft/airlock/internal/aiclient"
)

// Name is the skill's name and directory.
const Name = "airlock"

//go:embed SKILL.md
var content []byte

// userHomeDir and getenv locate the user-level skill directories; overridden in tests.
var (
	userHomeDir = os.UserHomeDir
	getenv      = os.Getenv
)

// detectedBy are the entries of a project telling that it uses a client.
var detectedBy = map[aiclient.Client][]string{
	aiclient.Claude: {".claude", ".mcp.json"},
	aiclient.VSCode: {".vscode"},
	aiclient.Codex:  {".codex"},
	aiclient.Cursor: {".cursor"},
	aiclient.Gemini: {".gemini"},
}

// Targets resolves sel (the "mcp.clients" setting) in projectDir: the project scope of the clients
// the project uses when sel detects them, then the targets sel names.
func Targets(projectDir string, sel aiclient.Selection) []aiclient.Target {
	var targets []aiclient.Target
	if sel.Detect {
		for _, client := range aiclient.Clients {
			if slices.ContainsFunc(detectedBy[client], func(name string) bool { return exists(filepath.Join(projectDir, name)) }) {
				targets = append(targets, aiclient.Target{Client: client, Scope: aiclient.ScopeProject})
			}
		}
	}
	return append(targets, sel.Targets...)
}

// Paths returns the SKILL.md files of targets, in the project directory or the user's home;
// clients sharing a directory share the file.
func Paths(projectDir string, targets []aiclient.Target) ([]string, error) {
	var paths []string
	for _, target := range targets {
		dir, err := skillsDir(projectDir, target.Scope == aiclient.ScopeUser, target.Client)
		if err != nil {
			return nil, err
		}
		if path := filepath.Join(dir, Name, "SKILL.md"); !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// File is a SKILL.md location with the clients reading it.
type File struct {
	Path    string
	Clients []aiclient.Client
}

// Files returns every skill file of the clients: the project ones, then the user-level ones.
func Files(projectDir string) ([]File, error) {
	var files []File
	for _, scope := range []aiclient.Scope{aiclient.ScopeProject, aiclient.ScopeUser} {
		for _, client := range aiclient.Clients {
			paths, err := Paths(projectDir, []aiclient.Target{{Client: client, Scope: scope}})
			if err != nil {
				return nil, err
			}
			idx := slices.IndexFunc(files, func(f File) bool { return f.Path == paths[0] })
			if idx < 0 {
				files = append(files, File{Path: paths[0]})
				idx = len(files) - 1
			}
			files[idx].Clients = append(files[idx].Clients, client)
		}
	}
	return files, nil
}

// UserInstalled returns the user-level airlock skill directories holding nothing but SKILL.md,
// for airlock uninstall: a directory the user added files to is left alone.
func UserInstalled() ([]string, error) {
	targets := make([]aiclient.Target, len(aiclient.Clients))
	for idx, client := range aiclient.Clients {
		targets[idx] = aiclient.Target{Client: client, Scope: aiclient.ScopeUser}
	}
	paths, err := Paths("", targets)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, path := range paths {
		entries, err := os.ReadDir(filepath.Dir(path))
		if err == nil && len(entries) == 1 && entries[0].Name() == filepath.Base(path) {
			dirs = append(dirs, filepath.Dir(path))
		}
	}
	return dirs, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func skillsDir(projectDir string, user bool, client aiclient.Client) (string, error) {
	if !slices.Contains(aiclient.Clients, client) {
		return "", fmt.Errorf("unknown AI client %q", client)
	}
	if !user {
		if client == aiclient.Claude {
			return filepath.Join(projectDir, ".claude", "skills"), nil
		}
		return filepath.Join(projectDir, ".agents", "skills"), nil
	}
	// Claude Code keeps its user-level skills in CLAUDE_CONFIG_DIR, ~/.claude by default.
	if dir := getenv("CLAUDE_CONFIG_DIR"); client == aiclient.Claude && dir != "" {
		return filepath.Join(dir, "skills"), nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to locate the home directory: %w", err)
	}
	if client == aiclient.Claude {
		return filepath.Join(home, ".claude", "skills"), nil
	}
	return filepath.Join(home, ".agents", "skills"), nil
}

// Install writes the skill at paths and returns those it wrote: a file already holding this
// version of the skill is left alone.
func Install(paths []string) ([]string, error) {
	var written []string
	var errs []error
	for _, path := range paths {
		if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, content) { // #nosec G304 -- a skill path built by Paths.
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			errs = append(errs, err)
			continue
		}
		// The skill is meant to be committed and read by every AI client: not a secret.
		if err := os.WriteFile(path, content, 0o644); err != nil { // #nosec G306 -- see above.
			errs = append(errs, err)
			continue
		}
		written = append(written, path)
	}
	return written, errors.Join(errs...)
}
