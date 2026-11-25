package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// A client that gives up (its own request timeout) must cancel the call: the SDK's stateless mode
// does not, and a long query then keeps its session busy for nobody.
func TestClientHangUpCancelsTheCall(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	server.AddReceivingMiddleware(mcpserve.CancelWith(httpRequestContext))
	canceled := make(chan struct{})
	mcp.AddTool(server, &mcp.Tool{Name: "slow"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
		<-ctx.Done()
		close(canceled)
		return nil, struct{}{}, ctx.Err()
	})
	g := &Gateway{cfg: &Config{}, token: "secret"}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(g.authorize(handler))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	transport := &mcp.StreamableClientTransport{Endpoint: ts.URL + endpointPath, HTTPClient: &http.Client{Transport: bearer{next: http.DefaultTransport, token: "secret"}}}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()                          //nolint:errcheck // a test client.
	_, _ = client.CallTool(ctx, &mcp.CallToolParams{Name: "slow"}) //nolint:errcheck // it times out.
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the call kept running after the client gave up")
	}
}
