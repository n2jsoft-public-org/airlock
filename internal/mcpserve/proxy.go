package mcpserve

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/version"
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
	upstream.local = mcp.NewServer(info.ServerInfo, &mcp.ServerOptions{Instructions: info.Instructions})
	if err := upstream.syncTools(ctx, session); err != nil {
		return err
	}
	return upstream.local.Run(ctx, local)
}

// upstream is the relayed server, reconnected when it dropped the session (idle timeout, restart).
type upstream struct {
	current *mcp.ClientSession
	// local serves the tools of the current session to the stdio client.
	local *mcp.Server
	// tools are the tools local serves, by name.
	tools map[string]*mcp.Tool
	url   string
	token string
	mu    sync.Mutex
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
	// A new session may come from a restarted server (airlock update) with other tools.
	// ponytail: tools only refresh on a reconnection, so after the next tool call; add a
	// keep-alive watcher if clients must see them before calling any tool.
	if u.local != nil {
		if err = u.syncTools(ctx, session); err != nil {
			_ = session.Close() //nolint:errcheck // the sync error is the one to report.
			return nil, err
		}
	}
	u.current = session
	return session, nil
}

// syncTools serves the tools of session on local. local notifies its client of any change
// (notifications/tools/list_changed), which then lists the tools again without a restart.
func (u *upstream) syncTools(ctx context.Context, session *mcp.ClientSession) error {
	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("failed to list the tools of %s: %w", u.url, err)
		}
		tools = append(tools, tool)
	}
	stale := u.tools
	u.tools = make(map[string]*mcp.Tool, len(tools))
	for _, tool := range tools {
		u.tools[tool.Name] = tool
		if !reflect.DeepEqual(stale[tool.Name], tool) {
			u.local.AddTool(tool, u.callTool)
		}
		delete(stale, tool.Name)
	}
	if len(stale) > 0 {
		u.local.RemoveTools(slices.Collect(maps.Keys(stale))...)
	}
	return nil
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
