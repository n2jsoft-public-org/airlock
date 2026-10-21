package mcpserve

import (
	"context"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startProxy relays endpoint to an in-memory client session, closed when the test ends.
func startProxy(t *testing.T, endpoint Endpoint) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	local, remote := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- Proxy(ctx, endpoint.URL, endpoint.Token, local) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "desktop"}, nil).Connect(ctx, remote, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close() //nolint:errcheck // the proxy returns once its side is closed.
		cancel()
		<-done //nolint:errcheck // canceled: the proxy returns an error, as expected.
	})
	return session
}

func TestProxyRelaysTheToolsOfTheServer(t *testing.T) {
	host := NewHost()
	endpoint := mountTestServer(t, host, "acme-dev-app-abc123")
	unmountOnCleanup(t, host, "acme-dev-app-abc123")

	session := startProxy(t, endpoint)
	if got := toolNames(t, session); !reflect.DeepEqual(got, []string{"ping"}) {
		t.Fatalf("tools = %v", got)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "ping", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("ping = %+v, %v", result, err)
	}
	if info := session.InitializeResult().ServerInfo; info.Name != "test" {
		t.Fatalf("server info = %+v, want the relayed server's", info)
	}
}

func TestProxyReconnectsWhenTheSessionIsGone(t *testing.T) {
	host := NewHost()
	endpoint := mountTestServer(t, host, "acme-dev-app-abc123")
	unmountOnCleanup(t, host, "acme-dev-app-abc123")
	up := &upstream{url: endpoint.URL, token: endpoint.Token}
	t.Cleanup(up.close)

	first, err := up.session(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := up.callTool(t.Context(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "ping", Arguments: []byte("{}")}})
	if err != nil || result.IsError {
		t.Fatalf("ping after the session was dropped = %+v, %v", result, err)
	}
	if up.current == first {
		t.Fatal("the dropped session must be replaced")
	}
}

func TestProxyFailsWithoutTheServer(t *testing.T) {
	local, _ := mcp.NewInMemoryTransports()
	if err := Proxy(t.Context(), "http://127.0.0.1:1/mcp/gone", "tok", local); err == nil {
		t.Fatal("a server that cannot be reached must fail the proxy")
	}
}
