package dbmcp

import (
	"fmt"

	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// DirectConfig describes a database reached directly, without tunnel, signed in with a password.
type DirectConfig struct {
	// OnPasswordRejected is called once when the database rejects Password, e.g. to forget a
	// password remembered in the keychain. Nil when unused.
	OnPasswordRejected func()
	ProjectDir         string
	// ServerPrefix starts the client-facing server name (mcpserve.DefaultServerPrefix when empty).
	ServerPrefix string
	// Name is the direct database name: it ends the server name and is the audit log target.
	Name string
	// DBName is the database signed in to when none is named; empty applies the engine default.
	DBName   string
	User     string
	Password string
	// LogDir receives the audit log of the MCP tool calls; empty disables it.
	LogDir  string
	Engine  Engine
	Address Address
	// SkipRegistration serves without writing the AI client files (see mcpserve.ServeConfig).
	SkipRegistration bool
	AllowWrites      bool
}

// StartDirect mounts the MCP server of a direct database on host and registers it in the
// project's AI clients. Its audit events carry no AWS profile nor region.
func StartDirect(host *mcpserve.Host, cfg *DirectConfig) (*Handle, error) {
	if !cfg.Engine.direct() {
		return nil, fmt.Errorf("%w: %s", ErrEngineUnsupported, cfg.Engine)
	}

	defaultDatabase := defaultDatabaseFor(cfg.Engine, cfg.DBName)
	password := newPasswordProvider(cfg.User, cfg.Password, defaultDatabase)
	password.onRejected = cfg.OnPasswordRejected
	handle, err := serveDatabase(host, &served{
		creds: &switchableProvider{current: password}, addr: cfg.Address, server: ServerConfig{
			Name: mcpserve.DirectServerName(cfg.ServerPrefix, cfg.Name), Target: cfg.Name,
			Via:    "reached directly at " + cfg.Address.String() + ".",
			Engine: cfg.Engine, AllowWrites: cfg.AllowWrites,
		},
		projectDir: cfg.ProjectDir, logDir: cfg.LogDir, skipRegistration: cfg.SkipRegistration,
	})
	if err != nil {
		return nil, err
	}
	handle.defaultDatabase = defaultDatabase
	return handle, nil
}
