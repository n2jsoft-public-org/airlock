package daemon

import (
	"context"
	"errors"
	"sync"

	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

// GatewayEnv is what the daemon hands its MCP gateway.
type GatewayEnv struct {
	Manager *sessions.Manager
	// Activity resets the idle clock of the daemon: call it on each gateway request.
	Activity func()
	Log      func(format string, args ...any)
}

// Gateway is the MCP gateway the daemon serves (internal/gateway).
type Gateway interface {
	// Status is the gateway line of `airlock daemon status`.
	Status() string
	// StatusWithoutToken is Status without the bearer token, for daemon.log.
	StatusWithoutToken() string
	// Close stops serving; the client entries stay, valid for the next start.
	Close() error
	// Unregister removes the client entries (an explicit stop of the daemon).
	Unregister() error
}

// StartGatewayFunc starts the gateway once the sessions are served; an error leaves the daemon
// running without gateway.
type StartGatewayFunc func(ctx context.Context, env GatewayEnv) (Gateway, error)

// gatewayState is the gateway of a running daemon, for its status.
type gatewayState struct {
	gateway Gateway
	off     string
	mu      sync.Mutex
}

func (g *gatewayState) set(gateway Gateway, off string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gateway, g.off = gateway, off
}

func (g *gatewayState) status() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gateway != nil {
		return g.gateway.Status()
	}
	return g.off
}

// close closes the gateway, which then reads as off; unregister first removes its client entries.
func (g *gatewayState) close(unregister bool) error {
	g.mu.Lock()
	gateway := g.gateway
	g.gateway, g.off = nil, "off"
	g.mu.Unlock()
	if gateway == nil {
		return nil
	}
	var unregisterErr error
	if unregister {
		unregisterErr = gateway.Unregister()
	}
	return errors.Join(unregisterErr, gateway.Close())
}
