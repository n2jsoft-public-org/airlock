package jumphost

import (
	"context"
	"fmt"

	"github.com/n2jsoft/airlock/internal/cachemcp"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/dockerdb"
)

// resourceDockerCache is the session type of a Redis or Valkey container: its MCP server serves
// the cache tools.
const resourceDockerCache = "docker-cache"

// openDockerCache opens the cache of container c, signed in as c.User with password, or without
// auth when password is empty: no tunnel, no IDE data source, nothing remembered.
func (r *Runner) openDockerCache(options *Options, c *dockerdb.Container, password string) (*Session, error) {
	label := "DOCKER " + c.Name
	if err := r.registry.claim(label, c.Name); err != nil {
		return nil, err
	}
	var creds cachemcp.CredentialProvider
	auth, cli := "none", fmt.Sprintf("redis-cli -h %s -p %d", c.Host, c.Port)
	if password != "" {
		creds = cachemcp.PasswordCredentials(c.User, password)
		auth, cli = "password · "+c.User, cli+" --user "+c.User+" --askpass"
	}
	session := &Session{
		Label:        label,
		ResourceType: resourceDockerCache,
		Target:       c.Name,
		Details:      SessionDetails{ConnectedServer: c.Address(), RestartCommand: "airlock docker " + c.Name, Auth: auth},
		Intro:        []string{fmt.Sprintf("Docker container %s (%s) at %s", c.Name, c.Engine, c.Address()), "TLS: disable", cli},
	}
	if !options.SkipMCP {
		handle, display := r.startDockerCacheMCP(options, c, creds)
		r.attachMCP(session, handle, display)
	}
	session.SignIn = &dockerCacheSignIn{user: c.User, conn: cachemcp.ConnConfig{Credentials: creds, Host: c.Host, LocalPort: c.Port}}
	session.client = cacheCredentialsClient(dbmcp.Address{Host: c.Host, Port: c.Port, TLS: dbmcp.TLSDisable}, creds)
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp)
	return session, nil
}

// startDockerCacheMCP serves the MCP server of a Docker cache and returns its display line ("not
// started: …" on failure, which never fails the session).
func (r *Runner) startDockerCacheMCP(options *Options, c *dockerdb.Container, creds cachemcp.CredentialProvider) (handle *cachemcp.Handle, display string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	handle, err = cachemcpStart(r.mcpHost, &cachemcp.SessionConfig{
		Credentials: creds, ProjectDir: projectDir, Target: "docker " + c.Name, Engine: string(c.Engine),
		Via:          "a local Docker container reached directly at " + c.Address() + ".",
		ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir, Host: c.Host, LocalPort: c.Port,
		AllowWrites: options.MCPAllowWrites, SkipRegistration: options.SkipMCPRegistration,
	})
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	if handle == nil {
		return nil, ""
	}
	display = handle.Display()
	return handle, display
}

// dockerCacheSignIn checks a Docker cache with PING: its only candidate is the session's own
// password, or no auth.
type dockerCacheSignIn struct {
	user string
	conn cachemcp.ConnConfig
}

func (s *dockerCacheSignIn) Candidates(context.Context) ([]SignInCandidate, error) {
	label := "password"
	if s.conn.Credentials == nil {
		label = "no auth"
	}
	return []SignInCandidate{{Label: label, User: s.user}}, nil
}

func (s *dockerCacheSignIn) Check(ctx context.Context, _ SignInCandidate) error {
	return cacheSignInError(checkCacheSignIn(ctx, s.conn))
}

// Use does nothing: the only candidate is the session's own password.
func (*dockerCacheSignIn) Use(SignInCandidate, *Options) {}

// Forget never applies: a Docker cache has no secret to fall back from.
func (*dockerCacheSignIn) Forget(SignInCandidate, *Options) bool { return false }
