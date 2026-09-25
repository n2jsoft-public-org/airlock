package dbmcp

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// SessionConfig describes the tunneled RDS instance of one jumphost session.
type SessionConfig struct {
	AWS             AWSRunner
	ProjectDir      string
	Profile         string
	Region          string
	Target          string
	AWSEngine       string
	RDSHost         string
	DBName          string
	InstanceARN     string
	MasterSecretARN string
	// IAMUser is the read-only user signing in with RDS IAM tokens (PostgreSQL).
	IAMUser   string
	RDSPort   int
	LocalPort int
	// IAMAuth reports whether the RDS instance has AWS IAM database authentication enabled.
	// Read-only PostgreSQL access needs it (there is no other read-only credential for PostgreSQL).
	IAMAuth     bool
	AllowWrites bool
}

// Handle is a running session MCP server.
type Handle struct {
	release    func() string
	releaseMsg string
	// Display describes the server for the session intro and dashboard.
	Display     string
	releaseOnce sync.Once
}

// Close stops the server, closes its database connections and removes its client entries.
// It returns a message describing the cleanup. Safe to call repeatedly.
func (h *Handle) Close() string {
	h.releaseOnce.Do(func() { h.releaseMsg = h.release() })
	return h.releaseMsg
}

// Start mounts the MCP server of a session on host and registers it in the project's AI clients.
// It returns nil, nil when the engine is not supported.
func Start(host *Host, cfg *SessionConfig) (*Handle, error) {
	engine, ok := EngineFor(cfg.AWSEngine)
	if !ok {
		return nil, nil
	}

	name := serverName(cfg.Profile, cfg.Target)
	suffix, err := randomHex(3)
	if err != nil {
		return nil, err
	}
	slug := name + "-" + suffix

	querier := newQuerier(engine, credentialProvider(cfg, engine), cfg.LocalPort)
	server := NewServer(&ServerConfig{
		Name: name, Target: cfg.Target, Profile: cfg.Profile, Region: cfg.Region,
		Engine: engine, AllowWrites: cfg.AllowWrites,
	}, querier)
	endpoint, err := host.Mount(slug, server)
	if err != nil {
		return nil, errors.Join(err, querier.Close())
	}

	entry := ClientEntry{Name: name, URL: endpoint.URL, Token: endpoint.Token}
	written, registerErr := RegisterClients(cfg.ProjectDir, entry)

	where := "no AI client config found; header Authorization: Bearer " + endpoint.Token
	if len(written) > 0 {
		where = relativeTo(cfg.ProjectDir, written)
	}
	display := fmt.Sprintf("MCP %s -> %s (%s)", name, endpoint.URL, where)
	if cfg.AllowWrites {
		display = "WRITES ENABLED · " + display
	}
	if registerErr != nil {
		display += fmt.Sprintf(" [warning: %v]", registerErr)
	}

	return &Handle{
		Display: display,
		release: func() string {
			removed, unregisterErr := UnregisterClients(cfg.ProjectDir, entry)
			if err := errors.Join(unregisterErr, host.Unmount(slug), querier.Close()); err != nil {
				return fmt.Sprintf("Warning: MCP server %q stopped with errors: %v", name, err)
			}
			if len(removed) == 0 {
				return fmt.Sprintf("MCP server %q stopped", name)
			}
			return fmt.Sprintf("MCP server %q stopped and removed from %s", name, relativeTo(cfg.ProjectDir, removed))
		},
	}, nil
}

func credentialProvider(cfg *SessionConfig, engine Engine) CredentialProvider {
	defaultDatabase := cfg.DBName
	if defaultDatabase == "" && engine == EnginePostgres {
		defaultDatabase = "postgres"
	}
	switch {
	case cfg.AllowWrites:
		if engine == EngineSQLServer {
			defaultDatabase = "master"
		}
		return newMasterSecretProvider(cfg.AWS, cfg.Target, cfg.MasterSecretARN, defaultDatabase)
	case engine == EngineSQLServer:
		return newSupportSecretsProvider(cfg.AWS, cfg.Target, cfg.InstanceARN)
	case !cfg.IAMAuth:
		return newIAMDisabledProvider(cfg.Target)
	default:
		return newIAMTokenProvider(cfg.AWS, cfg.RDSHost, cfg.RDSPort, cfg.IAMUser, defaultDatabase)
	}
}

// serverName is the stable client-facing name of a session's server, e.g. acme-dev-archive-db.
func serverName(profile, target string) string {
	return "acme-" + slugify(profile) + "-" + slugify(target)
}

func slugify(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func relativeTo(base string, paths []string) string {
	relative := make([]string, 0, len(paths))
	for _, path := range paths {
		if rel, err := filepath.Rel(base, path); err == nil {
			path = rel
		}
		relative = append(relative, path)
	}
	return strings.Join(relative, ", ")
}
