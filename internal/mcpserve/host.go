package mcpserve

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/aiclient"
)

const (
	routePrefix = "/mcp/"
	// idleSessionTimeout closes MCP sessions whose client went away without saying so.
	idleSessionTimeout = 30 * time.Minute
)

// Endpoint is where an AI client reaches one mounted MCP server.
type Endpoint struct {
	URL   string
	Token string
}

type route struct {
	handler http.Handler
	// server is closed with the route: its sessions would wait for idleSessionTimeout otherwise.
	server *mcp.Server
	token  string
}

// Host serves the MCP servers of all sessions of this process on one localhost listener.
// The listener starts with the first mounted server and stops after the last one is unmounted.
// It is safe for concurrent use.
type Host struct {
	// listen opens the listener on a random localhost port.
	listen func() (net.Listener, error)
	// listener is closed by Unmount itself: server.Close misses it until Serve has started.
	listener net.Listener
	server   *http.Server
	routes   map[string]route
	baseURL  string
	// clients are the AI clients the servers are registered in.
	clients aiclient.Selection
	mu      sync.Mutex
}

// NewHost returns a host listening on a random port of 127.0.0.1 once a server is mounted, whose
// servers are registered in the AI clients the project uses.
func NewHost() *Host {
	return NewHostFor(aiclient.AutoDetect)
}

// NewHostFor is NewHost registering the servers in clients.
func NewHostFor(clients aiclient.Selection) *Host {
	return &Host{
		listen:  func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		routes:  map[string]route{},
		clients: clients,
	}
}

// Mount serves server at /mcp/<slug>, protected by a new random bearer token.
func (h *Host) Mount(slug string, server *mcp.Server) (Endpoint, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.routes[slug]; exists {
		return Endpoint{}, fmt.Errorf("MCP route %q is already mounted", slug)
	}
	token, err := randomHex(32)
	if err != nil {
		return Endpoint{}, err
	}
	if h.server == nil {
		if err := h.startLocked(); err != nil {
			return Endpoint{}, err
		}
	}

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{SessionTimeout: idleSessionTimeout},
	)
	h.routes[slug] = route{handler: handler, server: server, token: token}
	return Endpoint{URL: h.baseURL + routePrefix + slug, Token: token}, nil
}

// Unmount stops serving slug and closes the MCP sessions of its server, and stops the listener
// when no route is left.
func (h *Host) Unmount(slug string) error {
	h.mu.Lock()
	server := h.routes[slug].server
	delete(h.routes, slug)
	err := h.stopIfIdleLocked()
	h.mu.Unlock()

	if server != nil {
		for session := range server.Sessions() {
			_ = session.Close() //nolint:errcheck // the client may be gone already.
		}
	}
	return err
}

// stopIfIdleLocked stops the listener when no route is left.
func (h *Host) stopIfIdleLocked() error {
	if len(h.routes) > 0 || h.server == nil {
		return nil
	}
	err := h.server.Close()
	if closeErr := h.listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		err = errors.Join(err, closeErr)
	}
	h.server, h.listener, h.baseURL = nil, nil, ""
	return err
}

func (h *Host) startLocked() error {
	listener, err := h.listen()
	if err != nil {
		return fmt.Errorf("failed to listen for MCP clients: %w", err)
	}
	h.listener = listener
	h.server = &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	h.baseURL = "http://" + listener.Addr().String()
	server := h.server
	go func() { _ = server.Serve(listener) }() //nolint:errcheck // Serve only returns once the host is closed.
	return nil
}

// ServeHTTP routes a request to its session's MCP handler after checking the bearer token.
func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slug, ok := strings.CutPrefix(r.URL.Path, routePrefix)
	h.mu.Lock()
	current, found := h.routes[strings.TrimSuffix(slug, "/")]
	h.mu.Unlock()
	if !ok || !found {
		http.NotFound(w, r)
		return
	}

	expected := "Bearer " + current.token
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return
	}
	current.handler.ServeHTTP(w, r)
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate random value: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
