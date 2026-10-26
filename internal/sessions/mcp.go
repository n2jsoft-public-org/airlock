package sessions

import (
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// ErrNoMCPServer reports a session without MCP server: none for its engine, or not started.
var ErrNoMCPServer = errors.New("no MCP server")

// ErrSessionEnded reports a session that is not running any more.
var ErrSessionEnded = errors.New("session ended")

// MCPAccess is what the MCP gateway needs to relay to the server of a session.
type MCPAccess struct {
	Server *mcp.Server
	// ResourceType is the session's (rds, eks, elasticache, direct).
	ResourceType string
	Writes       bool
}

// MCPServer returns the MCP server of the running session id, for the gateway of the same process.
func (m *Manager) MCPServer(id ID) (MCPAccess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(id)
	if e == nil {
		return MCPAccess{}, ErrUnknownSession
	}
	if !e.info.Active() {
		if e.info.Err != "" {
			return MCPAccess{}, fmt.Errorf("%w: %s", ErrSessionEnded, e.info.Err)
		}
		return MCPAccess{}, ErrSessionEnded
	}
	provider, ok := e.session.MCPServer().(mcpserve.ServerProvider)
	if !ok || provider.Server() == nil {
		return MCPAccess{}, ErrNoMCPServer
	}
	return MCPAccess{Server: provider.Server(), ResourceType: e.info.ResourceType, Writes: e.info.MCPWrites()}, nil
}

// Log appends texts to the log of session id, for the gateway's decisions.
func (m *Manager) Log(id ID, texts ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(id)
	if e == nil {
		return ErrUnknownSession
	}
	m.lines(e, texts...)
	return nil
}
