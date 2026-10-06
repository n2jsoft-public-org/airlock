package cachemcp

import (
	"sync"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// SessionConfig describes the tunneled cache of one ElastiCache session.
type SessionConfig struct {
	// Credentials signs the connections in; nil for caches without auth.
	Credentials CredentialProvider
	// OnRejected is called once when the cache refuses Credentials, e.g. to forget a password
	// remembered in the keychain; nil when unused.
	OnRejected func()
	ProjectDir string
	Profile    string
	Region     string
	// Target is the cache name: replication group, cluster or serverless cache.
	Target string
	// Engine is the ElastiCache engine; Memcached gets no server.
	Engine string
	// Endpoint is the real cache host, the TLS server name.
	Endpoint string
	// ServerPrefix starts the client-facing server name (mcpserve.DefaultServerPrefix when empty).
	ServerPrefix string
	// LogDir receives the audit log of the tool calls; empty disables it.
	LogDir    string
	LocalPort int
	// SkipRegistration serves without writing the AI client files (see mcpserve.ServeConfig).
	SkipRegistration bool
	TLS              bool
	// ClusterMode reports a cluster-mode-enabled replication group: one shard is reachable.
	ClusterMode bool
	// AllowWrites exposes the execute tool; the cache user's ACL still decides what is allowed.
	AllowWrites bool
}

// Handle is a running cache MCP server.
type Handle struct {
	*mcpserve.Handle
	cache *cache
}

// UseCredentials signs the next connections in with p; the open ones are closed.
func (h *Handle) UseCredentials(p CredentialProvider) error {
	return h.cache.clients.setCredentials(p)
}

// Start mounts the MCP server of a cache session on host and registers it in the project's AI
// clients. It returns nil, nil for engines without server (Memcached).
func Start(host *mcpserve.Host, cfg *SessionConfig) (*Handle, error) {
	engine, ok := EngineFor(cfg.Engine)
	if !ok {
		return nil, nil
	}
	// The -cache suffix keeps the name apart from an RDS instance of the same name on the profile.
	name := mcpserve.ServerName(cfg.ServerPrefix, cfg.Profile, cfg.Target) + "-cache"
	recorder := mcpserve.NewRecorder(cfg.LogDir)
	c := &cache{
		clients:     newClients(ConnConfig{Credentials: cfg.Credentials, ServerName: cfg.Endpoint, LocalPort: cfg.LocalPort, TLS: cfg.TLS}),
		clusterMode: cfg.ClusterMode,
	}
	if cfg.OnRejected != nil {
		var once sync.Once
		c.rejected = func() { once.Do(cfg.OnRejected) }
	}
	server := NewServer(&ServerConfig{
		Name: name, Target: cfg.Target, Profile: cfg.Profile, Region: cfg.Region, Engine: engine,
		AllowWrites: cfg.AllowWrites, ClusterMode: cfg.ClusterMode,
	}, c, recorder.Record)
	shared, err := mcpserve.Serve(host, &mcpserve.ServeConfig{
		Server: server, Recorder: recorder, Cleanup: c.clients.close,
		Name: name, ProjectDir: cfg.ProjectDir, AllowWrites: cfg.AllowWrites,
		SkipRegistration: cfg.SkipRegistration,
	})
	if err != nil {
		return nil, err
	}
	return &Handle{Handle: shared, cache: c}, nil
}
