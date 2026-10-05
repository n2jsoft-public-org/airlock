package gateway

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/aiclient"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

type bearer struct {
	next  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	r.Header.Set("User-Agent", "claude-code/2.1.0")
	return b.next.RoundTrip(r)
}

// claudeFile is the content of the isolated user-level Claude file ("" when missing).
func claudeFile(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, ".claude.json")) //nolint:gosec // a test path.
	if err != nil {
		return ""
	}
	return string(raw)
}

func startTestGateway(t *testing.T, addr string, activity *atomic.Int64) (g *Gateway, tokenDir string) {
	t.Helper()
	m, _ := newTestManager(t, sessionstest.NewRunner())
	tokenDir = t.TempDir()
	sel, err := aiclient.Parse("claude:user")
	if err != nil {
		t.Fatal(err)
	}
	g, err = Start(t.Context(), &Config{
		Manager: m, Settings: func() Settings { return Settings{} }, Discovery: func() (Discovery, error) { return &fakeDiscovery{}, nil },
		Activity: func() { activity.Add(1) }, Clients: sel, Name: "airlock", TokenDir: tokenDir,
		Listen: func() (net.Listener, error) { return net.Listen("tcp", addr) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() }) //nolint:errcheck // best-effort cleanup.
	return g, tokenDir
}

func TestGatewayServesAuthenticatedStatelessHTTP(t *testing.T) {
	claudeDir := isolateClients(t)
	var activity atomic.Int64
	g, tokenDir := startTestGateway(t, "127.0.0.1:0", &activity)
	raw, err := os.ReadFile(filepath.Join(tokenDir, TokenFileName)) //nolint:gosec // a test path.
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))

	resp, err := http.Post(g.URL(), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close() //nolint:errcheck // a test response.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", resp.StatusCode)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: g.URL(), DisableStandaloneSSE: true,
		HTTPClient: &http.Client{Transport: bearer{next: http.DefaultTransport, token: token}}}
	cs, err := client.Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }() //nolint:errcheck // best-effort cleanup.
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) < 20 {
		t.Fatalf("tools = %d, %v", len(tools.Tools), err)
	}
	if activity.Load() == 0 {
		t.Fatal("requests must count as daemon activity")
	}
	if !strings.Contains(g.Status(), "registered in") {
		t.Fatalf("status = %q", g.Status())
	}
	if raw := claudeFile(claudeDir); !strings.Contains(raw, g.URL()) {
		t.Fatalf("entry not written: %s", raw)
	}

	// A restart on the same port with the same token keeps the client working.
	addr := strings.TrimSuffix(strings.TrimPrefix(g.URL(), "http://"), "/mcp")
	if closeErr := g.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if raw := claudeFile(claudeDir); !strings.Contains(raw, g.URL()) {
		t.Fatalf("the entry must stay when the gateway closes: %s", raw)
	}
	restarted, err := Start(t.Context(), &Config{
		Manager: g.tools.manager, Settings: func() Settings { return Settings{} }, Discovery: func() (Discovery, error) { return &fakeDiscovery{}, nil },
		Clients: g.cfg.Clients, Name: "airlock", TokenDir: tokenDir, Listen: func() (net.Listener, error) { return net.Listen("tcp", addr) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }() //nolint:errcheck // best-effort cleanup.
	if _, err := cs.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("the client must keep working across a restart: %v", err)
	}
	if err := restarted.Unregister(); err != nil {
		t.Fatal(err)
	}
	if raw := claudeFile(claudeDir); strings.Contains(raw, g.URL()) {
		t.Fatalf("Unregister must remove the entry: %s", raw)
	}
}

func TestGatewayPortInUse(t *testing.T) {
	isolateClients(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }() //nolint:errcheck // best-effort cleanup.
	m, _ := newTestManager(t, sessionstest.NewRunner())
	_, err = Start(t.Context(), &Config{Manager: m, Settings: func() Settings { return Settings{} },
		Discovery: func() (Discovery, error) { return &fakeDiscovery{}, nil }, Name: "airlock", TokenDir: t.TempDir(),
		Port: busy.Addr().(*net.TCPAddr).Port}) //nolint:errcheck // the assertion is on the error only.
	if err == nil || !strings.Contains(err.Error(), "cannot listen") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayAuditEventsReachTheDaemonLog(t *testing.T) {
	isolateClients(t)
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	agent := openDirect(t, m, runner, "orders", nil, false, "claude-code")
	tokenDir := t.TempDir()
	var mu sync.Mutex
	var lines []string
	g, err := Start(t.Context(), &Config{
		Manager: m, Settings: func() Settings { return Settings{} }, Discovery: func() (Discovery, error) { return &fakeDiscovery{}, nil },
		Name: "airlock", TokenDir: tokenDir,
		Listen: func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		Log: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() }) //nolint:errcheck // best-effort cleanup.
	server := mcp.NewServer(&mcp.Implementation{Name: "airlock", Version: "1"}, nil)
	g.tools.add(server)
	if _, isError := callTool(t, connect(t, server), "close_session", map[string]any{"session": int(agent)}); isError {
		t.Fatal("close_session failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(lines, "\n"), `MCP gateway close_session "orders": closed`) {
		t.Fatalf("log lines = %q", lines)
	}
}

// F4: a request without the right token is refused before it counts as daemon activity.
func TestGatewayRefusesAWrongToken(t *testing.T) {
	isolateClients(t)
	var activity atomic.Int64
	g, _ := startTestGateway(t, "127.0.0.1:0", &activity)
	for _, header := range []string{"", "Bearer wrong", "Bearer " + strings.Repeat("0", 64), "Basic " + g.token, g.token} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, g.URL(), strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close() //nolint:errcheck // a test response.
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", header, resp.StatusCode)
		}
	}
	if n := activity.Load(); n != 0 {
		t.Fatalf("rejected requests counted as activity %d times", n)
	}
}
