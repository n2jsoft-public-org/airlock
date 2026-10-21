package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/aiclient"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// endpointPath is where the gateway answers.
const endpointPath = "/mcp"

const instructions = "airlock opens connections to private resources (RDS, direct and local Docker databases and caches, EKS clusters, " +
	"ElastiCache caches, DynamoDB tables, S3 buckets, CloudWatch Logs) and serves their tools. Call list_sessions to see what is open, list_targets to see what can " +
	"be opened, and open_session to open a target: the user may have to approve it in the airlock dashboard. The " +
	"db_, k8s_, cache_, dynamo_, s3_ and logs_ tools take the id of a session. Close the sessions you opened with close_session when done."

// Config is what the gateway runs with; internal/cli builds it from config.json.
type Config struct {
	Manager *sessions.Manager
	// Settings returns the settings of the next request; Discovery the runner listing the targets.
	Settings  func() Settings
	Discovery func() (Discovery, error)
	// Databases finds the direct databases; nil when they are unavailable.
	Databases jumphost.DirectDatabases
	// Containers finds the database containers of the local Docker; nil when unavailable.
	Containers jumphost.DockerContainers
	// Activity is called on each authenticated request (the daemon's idle clock); nil ignores them.
	Activity func()
	Log      func(format string, args ...any)
	// Listen opens the listener; nil listens on 127.0.0.1:Port.
	Listen func() (net.Listener, error)
	Now    func() time.Time
	// Name is the entry name (mcpserve.GatewayName); TokenDir holds gateway-token; LogDir receives
	// the audit log (empty disables it).
	Name     string
	TokenDir string
	LogDir   string
	// Clients are the AI clients of mcpClients: the entry goes to their user-level files only.
	Clients aiclient.Selection
	Port    int
	// SweepEvery is how often idle agent sessions are looked for (a minute when zero).
	SweepEvery time.Duration
}

// Gateway is a running MCP gateway.
type Gateway struct {
	cfg       *Config
	tools     *toolset
	relay     *relay
	http      *http.Server
	reg       *registration
	cancel    context.CancelFunc
	closeErr  error
	url       string
	token     string
	closeOnce sync.Once
}

// Start listens, serves the gateway and registers its entry. A listener that cannot open is an
// error; a registration that fails only shows in Status.
func Start(ctx context.Context, cfg *Config) (*Gateway, error) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	token, created, err := loadToken(cfg.TokenDir)
	if err != nil {
		return nil, err
	}
	listen := cfg.Listen
	if listen == nil {
		listen = func() (net.Listener, error) { return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port)) }
	}
	listener, err := listen()
	if err != nil {
		return nil, fmt.Errorf("cannot listen on 127.0.0.1:%d (set mcp.gateway.port to another port): %w", cfg.Port, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	g := &Gateway{cfg: cfg, token: token, cancel: cancel, url: "http://" + listener.Addr().String() + endpointPath}
	g.relay = newRelay(ctx, cfg.Manager, now, cfg.Settings)
	recorder := mcpserve.NewRecorder(cfg.LogDir)
	record := func(e *mcpserve.ToolEvent) {
		recorder.Record(e)
		logf("MCP gateway %s %q: %s", e.Tool, e.Target, e.Decision)
	}
	g.relay.audit(cfg.Name, record)
	g.tools = &toolset{
		manager: cfg.Manager, relay: g.relay, settings: cfg.Settings, discovery: cfg.Discovery, databases: cfg.Databases, containers: cfg.Containers,
		record: record, now: now, name: cfg.Name, startTimeout: defaultStartTimeout,
	}
	server := mcp.NewServer(&mcp.Implementation{Name: cfg.Name, Version: serverVersion}, &mcp.ServerOptions{Instructions: instructions})
	g.tools.add(server)
	if err = g.relay.add(ctx, server); err != nil {
		cancel()
		return nil, errors.Join(err, listener.Close())
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	g.http = &http.Server{Handler: g.authorize(handler), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = g.http.Serve(listener) }() //nolint:errcheck // Close ends it.
	if created {
		logf("new MCP gateway token in %s", filepath.Join(cfg.TokenDir, TokenFileName))
	}
	g.reg = register(cfg.Clients, mcpserve.ClientEntry{Name: cfg.Name, URL: g.url, Token: token})
	if g.reg.err != nil {
		logf("MCP gateway registration failed: %v", g.reg.err)
	}
	go g.tools.sweepLoop(ctx, cfg.SweepEvery)
	return g, nil
}

// authorize serves next to the requests of endpointPath carrying the token.
func (g *Gateway) authorize(next http.Handler) http.Handler {
	expected := []byte("Bearer " + g.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != endpointPath {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if g.cfg.Activity != nil {
			g.cfg.Activity()
		}
		next.ServeHTTP(w, r)
	})
}

// URL is where the gateway answers.
func (g *Gateway) URL() string { return g.url }

// Status describes the gateway for `airlock daemon status`.
func (g *Gateway) Status() string { return g.reg.status() }

// StatusWithoutToken is Status without the bearer token, for daemon.log.
func (g *Gateway) StatusWithoutToken() string { return g.reg.statusWithoutToken() }

// Close stops serving. The entry stays in the client files: it is still valid when the daemon
// starts again (same URL and token). Safe to call repeatedly.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		g.closeErr = g.http.Close()
		g.cancel()
		g.relay.closeAll()
	})
	return g.closeErr
}

// Unregister removes the entry from the client files, on an explicit stop of the daemon.
func (g *Gateway) Unregister() error { return g.reg.remove() }
