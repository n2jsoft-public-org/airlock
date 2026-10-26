// Package projectstack reads .airlock.json, the file an application's repository commits to list
// the targets it needs, by stack. The file comes from a repository: it is untrusted input, read
// strictly, and it only names targets, never secrets nor settings.
package projectstack

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

// FileName is the name of the stack file.
const FileName = ".airlock.json"

// maxPort is the highest TCP port.
const maxPort = 65535

// Target is one target of a stack, with the stack's profile and region applied.
type Target struct {
	// Type is the connect command: rds, eks, elasticache, dynamodb, s3, logs, forward, direct, docker.
	Type string
	// Name is the target's name; host:port for a forward.
	Name     string
	DBUser   string
	DBSecret string
	Profile  string
	Region   string
	// Port is the local port; 0 leaves the usual choice.
	Port           int
	MCPAllowWrites bool
}

// File is a parsed stack file.
type File struct {
	stacks map[string][]Target
	// Path is where the file was read.
	Path    string
	Default string
}

// typeKeys are the keys each target type takes besides type.
var typeKeys = map[string][]string{
	"rds":         {"name", "port", "dbUser", "dbSecret", "mcpAllowWrites", "profile", "region"},
	"eks":         {"name", "port", "profile", "region"},
	"elasticache": {"name", "port", "dbUser", "dbSecret", "mcpAllowWrites", "profile", "region"},
	"dynamodb":    {"name", "mcpAllowWrites", "profile", "region"},
	"s3":          {"name", "mcpAllowWrites", "profile", "region"},
	"logs":        {"name", "profile", "region"},
	"forward":     {"host", "remotePort", "port", "profile", "region"},
	"direct":      {"name", "mcpAllowWrites"},
	"docker":      {"name", "mcpAllowWrites"},
}

var stackName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Find returns the stack file of dir: there, else in the nearest parent. The search stops after
// the directory holding .git, before home and at the root; "" when there is none.
func Find(dir, home string) (string, error) {
	dir, home = filepath.Clean(dir), filepath.Clean(home)
	for dir != home {
		path := filepath.Join(dir, FileName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", i18n.NewError("projectstack.read", path, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
	return "", nil
}

// Load reads and checks the stack file at path.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the stack file found in the user's project.
	if err != nil {
		return nil, i18n.NewError("projectstack.read", path, err)
	}
	return Parse(path, data)
}

// Parse checks data, the content of the stack file at path.
func Parse(path string, data []byte) (*File, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, i18n.NewError("projectstack.json", path, err)
	}
	file := &File{Path: path, stacks: map[string][]Target{}}
	for key := range top {
		if key != "stacks" && key != "default" {
			return nil, i18n.NewError("projectstack.top_key", path, key)
		}
	}
	if raw, ok := top["default"]; ok {
		if err := json.Unmarshal(raw, &file.Default); err != nil {
			return nil, i18n.NewError("projectstack.json", path, err)
		}
	}
	var stacks map[string]json.RawMessage
	if raw, ok := top["stacks"]; !ok {
		return nil, i18n.NewError("projectstack.no_stacks", path)
	} else if err := json.Unmarshal(raw, &stacks); err != nil {
		return nil, i18n.NewError("projectstack.json", path, err)
	}
	if len(stacks) == 0 {
		return nil, i18n.NewError("projectstack.no_stacks", path)
	}
	for name, raw := range stacks {
		if !stackName.MatchString(name) {
			return nil, i18n.NewError("projectstack.stack_name", path, name)
		}
		targets, err := parseStack(raw)
		if err != nil {
			return nil, i18n.NewError("projectstack.in_stack", path, name, err)
		}
		file.stacks[name] = targets
	}
	if file.Default != "" && file.stacks[file.Default] == nil {
		return nil, i18n.NewError("projectstack.default_unknown", path, file.Default, strings.Join(file.Names(), ", "))
	}
	return file, nil
}

// Names returns the stack names, sorted.
func (f *File) Names() []string { return slices.Sorted(maps.Keys(f.stacks)) }

// Stack returns the targets of the stack name; "" is the default stack, else the only one.
func (f *File) Stack(name string) (string, []Target, error) {
	if name == "" {
		name = f.Default
	}
	if names := f.Names(); name == "" && len(names) == 1 {
		name = names[0]
	}
	if name == "" {
		return "", nil, i18n.NewError("projectstack.no_default", f.Path, strings.Join(f.Names(), ", "))
	}
	targets, ok := f.stacks[name]
	if !ok {
		return "", nil, i18n.NewError("projectstack.stack_unknown", name, f.Path, strings.Join(f.Names(), ", "))
	}
	return name, targets, nil
}

// stackObject is the object form of a stack: targets sharing a profile and region.
type stackObject struct {
	Profile string            `json:"profile"`
	Region  string            `json:"region"`
	Targets []json.RawMessage `json:"targets"`
}

// parseStack reads a stack: a list of targets, or a stackObject.
func parseStack(raw json.RawMessage) ([]Target, error) {
	var stack stackObject
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(raw, &stack.Targets); err != nil {
			return nil, err
		}
	} else {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, i18n.NewError("projectstack.stack_shape")
		}
		for key := range keys {
			if key != "profile" && key != "region" && key != "targets" {
				return nil, i18n.NewError("projectstack.stack_key", key)
			}
		}
		if err := json.Unmarshal(raw, &stack); err != nil {
			return nil, err
		}
	}
	if len(stack.Targets) == 0 {
		return nil, i18n.NewError("projectstack.empty_stack")
	}
	targets := make([]Target, 0, len(stack.Targets))
	seen, ports := map[string]int{}, map[int]int{}
	for idx, raw := range stack.Targets {
		target, err := parseTarget(raw)
		if err != nil {
			return nil, i18n.NewError("projectstack.in_target", idx+1, err)
		}
		if target.Profile == "" && typeTakes(target.Type, "profile") {
			target.Profile = stack.Profile
		}
		if target.Region == "" && typeTakes(target.Type, "region") {
			target.Region = stack.Region
		}
		key := target.Type + " " + strings.ToLower(target.Name) + " " + target.Profile + " " + target.Region
		if first, ok := seen[key]; ok {
			return nil, i18n.NewError("projectstack.duplicate", idx+1, target.Type, target.Name, first)
		}
		seen[key] = idx + 1
		if first, ok := ports[target.Port]; ok && target.Port != 0 {
			return nil, i18n.NewError("projectstack.duplicate_port", idx+1, target.Port, first)
		}
		ports[target.Port] = idx + 1
		targets = append(targets, target)
	}
	return targets, nil
}

// targetObject is a target as written in the file.
type targetObject struct {
	Type           string `json:"type"`
	Name           string `json:"name"`
	Host           string `json:"host"`
	DBUser         string `json:"dbUser"`
	DBSecret       string `json:"dbSecret"`
	Profile        string `json:"profile"`
	Region         string `json:"region"`
	RemotePort     int    `json:"remotePort"`
	Port           int    `json:"port"`
	MCPAllowWrites bool   `json:"mcpAllowWrites"`
}

// parseTarget reads and checks one target.
func parseTarget(raw json.RawMessage) (Target, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return Target{}, err
	}
	var obj targetObject
	if err := json.Unmarshal(keys["type"], &obj.Type); err != nil || obj.Type == "" {
		return Target{}, i18n.NewError("projectstack.type_required", strings.Join(slices.Sorted(maps.Keys(typeKeys)), ", "))
	}
	allowed, ok := typeKeys[obj.Type]
	if !ok {
		return Target{}, i18n.NewError("projectstack.type_unknown", obj.Type, strings.Join(slices.Sorted(maps.Keys(typeKeys)), ", "))
	}
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		switch {
		case key == "type" || slices.Contains(allowed, key):
		case looksSecret(key) && !typeTakes("rds", key): // dbSecret names a secret, it holds none
			return Target{}, i18n.NewError("projectstack.secret_key", key)
		default:
			return Target{}, i18n.NewError("projectstack.unknown_key", key, obj.Type, strings.Join(allowed, ", "))
		}
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Target{}, err
	}
	target := Target{
		Type: obj.Type, Name: strings.TrimSpace(obj.Name), DBUser: strings.TrimSpace(obj.DBUser), DBSecret: strings.TrimSpace(obj.DBSecret),
		Profile: strings.TrimSpace(obj.Profile), Region: strings.TrimSpace(obj.Region), Port: obj.Port, MCPAllowWrites: obj.MCPAllowWrites,
	}
	if obj.Port < 0 || obj.Port > maxPort {
		return Target{}, i18n.NewError("projectstack.port", "port", obj.Port, maxPort)
	}
	if obj.Type == "forward" {
		if obj.RemotePort <= 0 || obj.RemotePort > maxPort {
			return Target{}, i18n.NewError("projectstack.port", "remotePort", obj.RemotePort, maxPort)
		}
		name, err := jumphost.ForwardTarget(obj.Host, strconv.Itoa(obj.RemotePort))
		if err != nil {
			return Target{}, err
		}
		target.Name = name
	}
	if target.Name == "" {
		return Target{}, i18n.NewError("projectstack.name_required")
	}
	return target, nil
}

// typeTakes reports whether targets of type take key.
func typeTakes(targetType, key string) bool { return slices.Contains(typeKeys[targetType], key) }

// looksSecret reports a key that would hold a secret: the file is committed.
func looksSecret(key string) bool {
	lower := strings.ToLower(key)
	for _, word := range []string{"pass", "pwd", "secret", "token", "key", "credential", "auth"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}
