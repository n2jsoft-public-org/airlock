package mcpserve

import (
	"errors"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Session is a running session MCP server, as the dashboard and the session lifecycle see it.
type Session interface {
	// Display describes the server (access mode, name, URL, config files) for the dashboard MCP row.
	Display() string
	// Subscribe calls fn with every tool call made through the server; fn must not block.
	Subscribe(fn func(ToolEvent))
	// QueryLogDir is where the tool calls are logged (empty when they are not).
	QueryLogDir() string
	// AllowsWrites reports whether the server offers a tool modifying data.
	AllowsWrites() bool
	// Close stops the server and removes its client entries, returning a message describing the
	// cleanup. Safe to call repeatedly.
	Close() string
}

// ServeConfig describes a server to mount for one session.
type ServeConfig struct {
	Server   *mcp.Server
	Recorder *Recorder
	// Cleanup closes the connections of the server once it stopped; nil when there are none.
	Cleanup func() error
	// Name is the client-facing name (see ServerName).
	Name       string
	ProjectDir string
	// AllowWrites reports whether Server offers a tool modifying data.
	AllowWrites bool
}

// Handle is a mounted and registered session MCP server.
type Handle struct {
	recorder    *Recorder
	release     func() string
	endpoint    Endpoint
	display     string
	releaseMsg  string
	releaseOnce sync.Once
	allowWrites bool
}

// Serve mounts cfg.Server on host under a unique slug and registers it in the project's AI clients.
func Serve(host *Host, cfg *ServeConfig) (*Handle, error) {
	suffix, err := randomHex(3)
	if err != nil {
		return nil, err
	}
	slug := cfg.Name + "-" + suffix
	endpoint, err := host.Mount(slug, cfg.Server)
	if err != nil {
		if cfg.Cleanup != nil {
			err = errors.Join(err, cfg.Cleanup())
		}
		return nil, err
	}

	entry := ClientEntry{Name: cfg.Name, URL: endpoint.URL, Token: endpoint.Token}
	written, registerErr := RegisterClients(cfg.ProjectDir, entry)
	where := "no AI client config found; header Authorization: Bearer " + endpoint.Token
	if len(written) > 0 {
		where = RelativeTo(cfg.ProjectDir, written)
	}
	accessLabel := "read-only"
	if cfg.AllowWrites {
		accessLabel = "WRITES ENABLED"
	}
	display := fmt.Sprintf("%s · %s -> %s (%s)", accessLabel, cfg.Name, endpoint.URL, where)
	if registerErr != nil {
		display += fmt.Sprintf(" [warning: %v]", registerErr)
	}

	return &Handle{
		recorder:    cfg.Recorder,
		endpoint:    endpoint,
		display:     display,
		allowWrites: cfg.AllowWrites,
		release: func() string {
			removed, unregisterErr := UnregisterClients(cfg.ProjectDir, entry)
			errs := []error{unregisterErr, host.Unmount(slug)}
			if cfg.Cleanup != nil {
				errs = append(errs, cfg.Cleanup())
			}
			if err := errors.Join(errs...); err != nil {
				return fmt.Sprintf("Warning: MCP server %q stopped with errors: %v", cfg.Name, err)
			}
			if len(removed) == 0 {
				return fmt.Sprintf("MCP server %q stopped", cfg.Name)
			}
			return fmt.Sprintf("MCP server %q stopped and removed from %s", cfg.Name, RelativeTo(cfg.ProjectDir, removed))
		},
	}, nil
}

// Display describes the server (access mode, name, URL, config files) for the dashboard MCP row.
func (h *Handle) Display() string { return h.display }

// Endpoint is where AI clients reach the server.
func (h *Handle) Endpoint() Endpoint { return h.endpoint }

// Subscribe calls fn with every tool call made through the server; fn must not block.
func (h *Handle) Subscribe(fn func(ToolEvent)) { h.recorder.Subscribe(fn) }

// QueryLogDir is where the tool calls are logged (empty when they are not).
func (h *Handle) QueryLogDir() string { return h.recorder.LogDir() }

// AllowsWrites reports whether the server offers a tool modifying data.
func (h *Handle) AllowsWrites() bool { return h.allowWrites }

// Close stops the server and removes its client entries. Safe to call repeatedly.
func (h *Handle) Close() string {
	h.releaseOnce.Do(func() { h.releaseMsg = h.release() })
	return h.releaseMsg
}
