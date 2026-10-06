package mcpserve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/version"
)

const (
	// ProxyCommand is the airlock subcommand relaying an HTTP MCP server to a stdio client.
	ProxyCommand = "mcp-proxy"
	// ProxyTokenEnv is the environment variable holding the bearer token of the relayed server.
	ProxyTokenEnv = "AIRLOCK_MCP_TOKEN"
)

// Proxy serves on local the tools of the HTTP MCP server at url, signing in with token, for the
// clients that only launch stdio servers (Claude Desktop). It returns when local is closed.
func Proxy(ctx context.Context, url, token string, local mcp.Transport) error {
	upstream := &upstream{url: url, token: token}
	defer upstream.close()
	session, err := upstream.session(ctx)
	if err != nil {
		return err
	}
	info := session.InitializeResult()
	server := mcp.NewServer(info.ServerInfo, &mcp.ServerOptions{Instructions: info.Instructions})
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("failed to list the tools of %s: %w", url, err)
		}
		server.AddTool(tool, upstream.callTool)
	}
	return server.Run(ctx, local)
}

// upstream is the relayed server, reconnected when it dropped the session (idle timeout, restart).
type upstream struct {
	current *mcp.ClientSession
	url     string
	token   string
	mu      sync.Mutex
}

func (u *upstream) session(ctx context.Context) (*mcp.ClientSession, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.current != nil {
		return u.current, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "airlock-" + ProxyCommand, Version: version.String()}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   u.url,
		HTTPClient: &http.Client{Transport: bearerTransport{token: u.token}},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", u.url, err)
	}
	u.current = session
	return session, nil
}

// reset drops session if it is still the current one, so that the next call reconnects.
func (u *upstream) reset(session *mcp.ClientSession) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.current == session {
		u.current = nil
		_ = session.Close() //nolint:errcheck // the session is already broken.
	}
}

func (u *upstream) close() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.current != nil {
		_ = u.current.Close() //nolint:errcheck // nothing is left to report a failure to.
		u.current = nil
	}
}

func (u *upstream) callTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	params := &mcp.CallToolParams{Name: req.Params.Name, Arguments: req.Params.Arguments}
	for attempt := 0; ; attempt++ {
		session, err := u.session(ctx)
		if err != nil {
			return nil, err
		}
		result, err := session.CallTool(ctx, params)
		dropped := errors.Is(err, mcp.ErrSessionMissing) || errors.Is(err, mcp.ErrConnectionClosed)
		if !dropped || attempt > 0 {
			return result, err
		}
		u.reset(session)
	}
}

// bearerTransport adds an Authorization header to every request.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
