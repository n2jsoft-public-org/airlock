// Package aiclient names the AI clients (coding agents and editors) airlock registers its MCP
// servers in, and parses the selection of the "mcpClients" setting.
package aiclient

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Client is an AI client airlock can register MCP servers in.
type Client string

const (
	Claude Client = "claude"
	VSCode Client = "vscode"
	Codex  Client = "codex"
	Cursor Client = "cursor"
	Gemini Client = "gemini"
)

// Clients lists every supported client, in the order their files are written.
var Clients = []Client{Claude, VSCode, Codex, Cursor, Gemini}

// Scope tells which configuration file of a client receives the servers.
type Scope string

const (
	// ScopeProject is the client's file in the project (working) directory.
	ScopeProject Scope = "project"
	// ScopeUser is the client's file in the user's home, seen by every project.
	ScopeUser Scope = "user"
)

// Target is one client configuration file to write: a client and a scope.
type Target struct {
	Client Client
	Scope  Scope
}

// String renders t as in the setting: the scope is left out for the project.
func (t Target) String() string {
	if t.Scope == ScopeProject {
		return string(t.Client)
	}
	return string(t.Client) + ":" + string(t.Scope)
}

const (
	// Auto selects the clients the project uses, detected from their directories.
	Auto = "auto"
	// None selects no client: servers are not registered anywhere.
	None = "none"
)

// Selection is the parsed "mcpClients" setting.
type Selection struct {
	// Targets are the files chosen explicitly.
	Targets []Target
	// Detect adds the project files of the clients the project uses.
	Detect bool
}

// AutoDetect is the default selection: the project files of the clients the project uses.
var AutoDetect = Selection{Detect: true}

// UserOnly returns the user-level targets of s, without detection: what a process outside any
// project (the daemon) can register in.
func (s Selection) UserOnly() Selection {
	var user Selection
	for _, target := range s.Targets {
		if target.Scope == ScopeUser {
			user.Targets = append(user.Targets, target)
		}
	}
	return user
}

// AllUser selects the user-level file of every client.
func AllUser() Selection {
	sel := Selection{Targets: make([]Target, 0, len(Clients))}
	for _, client := range Clients {
		sel.Targets = append(sel.Targets, Target{Client: client, Scope: ScopeUser})
	}
	return sel
}

// String renders s in its normalized setting form.
func (s Selection) String() string {
	parts := make([]string, 0, len(s.Targets)+1)
	if s.Detect {
		parts = append(parts, Auto)
	}
	for _, target := range s.Targets {
		parts = append(parts, target.String())
	}
	if len(parts) == 0 {
		return None
	}
	return strings.Join(parts, ",")
}

// Parse reads a comma-separated list of client[:scope] (scope project, the default, or user),
// where auto adds the detected project clients. Empty means auto; none selects nothing and stands
// alone. Case and spaces do not matter; duplicates are dropped.
func Parse(s string) (Selection, error) {
	var sel Selection
	none := false
	seen := map[Target]bool{}
	items := 0
	for item := range strings.SplitSeq(s, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		items++
		switch item {
		case Auto:
			sel.Detect = true
			continue
		case None:
			none = true
			continue
		}
		target, err := parseTarget(item)
		if err != nil {
			return Selection{}, err
		}
		if !seen[target] {
			seen[target] = true
			sel.Targets = append(sel.Targets, target)
		}
	}
	switch {
	case none && items > 1:
		return Selection{}, errors.New(`"none" cannot be combined with other clients`)
	case none:
		return Selection{}, nil
	case items == 0:
		return AutoDetect, nil
	}
	return sel, nil
}

func parseTarget(item string) (Target, error) {
	name, scope, hasScope := strings.Cut(item, ":")
	target := Target{Client: Client(strings.TrimSpace(name)), Scope: ScopeProject}
	if !slices.Contains(Clients, target.Client) {
		return Target{}, fmt.Errorf("unknown AI client %q (valid: %s, or %s / %s)", target.Client, clientNames(), Auto, None)
	}
	if hasScope {
		target.Scope = Scope(strings.TrimSpace(scope))
		if target.Scope != ScopeProject && target.Scope != ScopeUser {
			return Target{}, fmt.Errorf("unknown scope %q for %s (valid: %s, %s)", target.Scope, target.Client, ScopeProject, ScopeUser)
		}
	}
	return target, nil
}

func clientNames() string {
	names := make([]string, 0, len(Clients))
	for _, c := range Clients {
		names = append(names, string(c))
	}
	return strings.Join(names, ", ")
}
