package dbmcp

import (
	"errors"

	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// ErrReadOnlySecretTagUnset reports a read-only SQL Server session without a read-only secret tag
// configured: its credentials cannot be found.
var ErrReadOnlySecretTagUnset = errors.New(`no read-only secret tag configured for SQL Server (set "readOnlySecretTag")`)

// SessionConfig describes the tunneled RDS instance of one jumphost session.
type SessionConfig struct {
	// SkipRegistration serves without writing the AI client files (see mcpserve.ServeConfig).
	SkipRegistration bool
	AWS              AWSRunner
	ProjectDir       string
	Profile          string
	Region           string
	Target           string
	AWSEngine        string
	RDSHost          string
	DBName           string
	InstanceARN      string
	MasterSecretARN  string
	// ServerPrefix starts the client-facing server name (mcpserve.DefaultServerPrefix when empty).
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

// Handle is a running database MCP server.
type Handle struct {
	*mcpserve.Handle
	// creds is the provider of the server, switched by UseSecret; querier holds its pools.
	creds   *switchableProvider
	querier *sqlQuerier
	aws     AWSRunner
	// defaultDatabase is the database UseSecret signs in to when none is named.
	defaultDatabase string
}

// UseSecret makes the server sign in with the password of secretID from now on, as the secret's
// username, else as user. The connections opened with the former credentials are closed.
func (h *Handle) UseSecret(secretID, user string) error {
	h.creds.set(newSecretPasswordProvider(h.aws, secretID, user, h.defaultDatabase))
	return h.querier.reset()
}

// Start mounts the MCP server of a session on host and registers it in the project's AI clients.
// It returns nil, nil when the engine is not supported.
func Start(host *mcpserve.Host, cfg *SessionConfig) (*Handle, error) {
	engine, ok := EngineFor(cfg.AWSEngine)
	if !ok {
		return nil, nil
	}
	if engine == EngineSQLServer && !cfg.AllowWrites && cfg.DBPassword == "" && cfg.DBSecretARN == "" && cfg.ReadOnlySecretTag.IsZero() {
		return nil, ErrReadOnlySecretTagUnset
	}

	name := mcpserve.ServerName(cfg.ServerPrefix, cfg.Profile, cfg.Target)
	creds := &switchableProvider{current: credentialProvider(cfg, engine)}
	handle, err := serveDatabase(host, &served{
		creds: creds, addr: TunnelAddress(cfg.LocalPort), server: ServerConfig{
			Name: name, Target: cfg.Target, Profile: cfg.Profile, Region: cfg.Region,
			Engine: engine, AllowWrites: cfg.AllowWrites,
		},
		projectDir: cfg.ProjectDir, logDir: cfg.LogDir, skipRegistration: cfg.SkipRegistration,
	})
	if err != nil {
		return nil, err
	}
	handle.aws = cfg.AWS
	handle.defaultDatabase = defaultDatabaseFor(engine, cfg.DBName)
	return handle, nil
}

// served is what serveDatabase needs to mount the MCP server of one database.
type served struct {
	skipRegistration bool
	creds            *switchableProvider
	projectDir       string
	logDir           string
	server           ServerConfig
	addr             Address
}

// serveDatabase mounts the MCP server of a database on host: audit recording, querier and server.
// The AWS-only fields of the Handle are left for the caller.
func serveDatabase(host *mcpserve.Host, s *served) (*Handle, error) {
	access := "read-only"
	if s.server.AllowWrites {
		access = "read-write"
	}
	recorder := mcpserve.NewRecorder(s.logDir)
	inner := newQuerier(s.server.Engine, s.creds, s.addr)
	querier := newRecordingQuerier(inner, &mcpserve.ToolEvent{
		Server: s.server.Name, Profile: s.server.Profile, Region: s.server.Region,
		Target: s.server.Target, Engine: string(s.server.Engine), Access: access,
	}, recorder.Record)
	shared, err := mcpserve.Serve(host, &mcpserve.ServeConfig{
		Server: NewServer(&s.server, querier), Recorder: recorder, Cleanup: querier.Close,
		Name: s.server.Name, ProjectDir: s.projectDir, AllowWrites: s.server.AllowWrites,
		SkipRegistration: s.skipRegistration,
	})
	if err != nil {
		return nil, err
	}
	return &Handle{Handle: shared, creds: s.creds, querier: inner}, nil
}

func credentialProvider(cfg *SessionConfig, engine Engine) CredentialProvider {
	defaultDatabase := defaultDatabaseFor(engine, cfg.DBName)
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
