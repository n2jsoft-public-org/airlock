package dbmcp

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/n2jsoft/airlock/internal/awstag"
)

// DefaultServerPrefix starts server names when SessionConfig.ServerPrefix is empty.
const DefaultServerPrefix = "airlock"

// ErrReadOnlySecretTagUnset reports a read-only SQL Server session without a read-only secret tag
// configured: its credentials cannot be found.
var ErrReadOnlySecretTagUnset = errors.New(`no read-only secret tag configured for SQL Server (set "readOnlySecretTag")`)

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
	// ServerPrefix starts the client-facing server name (DefaultServerPrefix when empty).
	ServerPrefix string
	// ReadOnlySecretTag marks the read-only per-database secrets of SQL Server instances; zero
	// disables read-only SQL Server access.
	ReadOnlySecretTag awstag.Tag
	// LogDir receives the audit log of the MCP tool calls; empty disables it.
	LogDir string
	// DBUser signs in with RDS IAM tokens (PostgreSQL), or with DBPassword when it is set.
	DBUser string
	// DBPassword is the password entered by the user instead of AWS IAM sign-in; empty when unused.
	DBPassword string
	// DBSecretARN names the Secrets Manager secret holding the password (RDS JSON or plain text);
	// it wins over DBPassword and the automatic secrets. Empty when unused.
	DBSecretARN string
	// OnPasswordRejected is called once when the database rejects DBPassword, e.g. to forget a
	// password remembered in the keychain. Nil when unused.
	OnPasswordRejected func()
	RDSPort            int
	LocalPort          int
	// IAMAuth reports whether the RDS instance has AWS IAM database authentication enabled.
	// PostgreSQL access needs it unless DBPassword is set.
	IAMAuth bool
	// AllowWrites exposes the execute tool. PostgreSQL still signs in as DBUser, whose grants decide
	// what can be written; SQL Server, without AWS IAM sign-in, switches to the master user secret.
	AllowWrites bool
}

// Handle is a running session MCP server.
type Handle struct {
	release  func() string
	recorder *queryRecorder
	endpoint Endpoint
	// Display describes the server (access mode, name, URL, config files) for the dashboard MCP row.
	Display     string
	releaseMsg  string
	releaseOnce sync.Once
}

// Subscribe calls fn with every tool call made through the server, from the goroutine serving it:
// fn must not block.
func (h *Handle) Subscribe(fn func(QueryEvent)) {
	h.recorder.subscribe(fn)
}

// QueryLogDir is where the tool calls are logged (empty when they are not).
func (h *Handle) QueryLogDir() string {
	if h.recorder.log == nil {
		return ""
	}
	return h.recorder.log.Dir()
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
	if engine == EngineSQLServer && !cfg.AllowWrites && cfg.DBPassword == "" && cfg.DBSecretARN == "" && cfg.ReadOnlySecretTag.IsZero() {
		return nil, ErrReadOnlySecretTagUnset
	}

	name := serverName(cfg.ServerPrefix, cfg.Profile, cfg.Target)
	suffix, err := randomHex(3)
	if err != nil {
		return nil, err
	}
	slug := name + "-" + suffix

	access, accessLabel := "read-only", "read-only"
	if cfg.AllowWrites {
		access, accessLabel = "read-write", "WRITES ENABLED"
	}
	recorder := &queryRecorder{}
	if cfg.LogDir != "" {
		recorder.log = NewAuditLog(cfg.LogDir)
	}
	querier := newRecordingQuerier(newQuerier(engine, credentialProvider(cfg, engine), cfg.LocalPort), &QueryEvent{
		Server: name, Profile: cfg.Profile, Region: cfg.Region, Target: cfg.Target, Engine: engine, Access: access,
	}, recorder.record)
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
	display := fmt.Sprintf("%s · %s -> %s (%s)", accessLabel, name, endpoint.URL, where)
	if registerErr != nil {
		display += fmt.Sprintf(" [warning: %v]", registerErr)
	}

	return &Handle{
		Display:  display,
		recorder: recorder,
		endpoint: endpoint,
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
	case cfg.DBSecretARN != "":
		return newSecretPasswordProvider(cfg.AWS, cfg.DBSecretARN, cfg.DBUser, defaultDatabase)
	case cfg.DBPassword != "":
		provider := newPasswordProvider(cfg.DBUser, cfg.DBPassword, defaultDatabase)
		provider.onRejected = cfg.OnPasswordRejected
		return provider
	case engine == EngineSQLServer && cfg.AllowWrites:
		return newMasterSecretProvider(cfg.AWS, cfg.Target, cfg.MasterSecretARN, "master")
	case engine == EngineSQLServer:
		return newReadOnlySecretsProvider(cfg.AWS, cfg.Target, cfg.InstanceARN, cfg.ReadOnlySecretTag)
	case !cfg.IAMAuth:
		return newIAMDisabledProvider(cfg.Target)
	default:
		return newIAMTokenProvider(cfg.AWS, cfg.RDSHost, cfg.RDSPort, cfg.DBUser, defaultDatabase)
	}
}

// serverName is the stable client-facing name of a session's server, e.g. airlock-dev-archive-db.
// The prefix is not repeated when the profile already carries it (e.g. prefix team, profile team-prod).
func serverName(prefix, profile, target string) string {
	prefix = slugify(prefix)
	if prefix == "" {
		prefix = DefaultServerPrefix
	}
	name := slugify(profile)
	if name != prefix && !strings.HasPrefix(name, prefix+"-") {
		name = prefix + "-" + name
	}
	return name + "-" + slugify(target)
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
