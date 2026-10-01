package mcpserve

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearerTransport adds an Authorization header to every request.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func post(t *testing.T, url, authorization string) int {
	t.Helper()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func TestHostServesMountedServerToAuthorizedClient(t *testing.T) {
	host := NewHost()
	endpoint := mountTestServer(t, host, "acme-dev-app-abc123")
	t.Cleanup(func() { _ = host.Unmount("acme-dev-app-abc123") })

	if !strings.HasPrefix(endpoint.URL, "http://127.0.0.1:") || !strings.HasSuffix(endpoint.URL, "/mcp/acme-dev-app-abc123") {
		t.Fatalf("endpoint URL = %s", endpoint.URL)
	}
	if len(endpoint.Token) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(endpoint.Token))
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{token: endpoint.Token}, Timeout: 10 * time.Second},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if names := toolNames(t, session); len(names) != 1 {
		t.Fatalf("tools = %v", names)
	}
}

func TestHostRejectsMissingOrWrongToken(t *testing.T) {
	host := NewHost()
	endpoint := mountTestServer(t, host, "a")
	t.Cleanup(func() { _ = host.Unmount("a") })

	if status := post(t, endpoint.URL, ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", status)
	}
	if status := post(t, endpoint.URL, "Bearer wrong"); status != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", status)
	}
	if status := post(t, endpoint.URL, "Bearer "+endpoint.Token); status != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", status)
	}
}

func TestHostTokensArePerRoute(t *testing.T) {
	host := NewHost()
	first := mountTestServer(t, host, "a")
	second := mountTestServer(t, host, "b")
	t.Cleanup(func() { _ = host.Unmount("a"); _ = host.Unmount("b") })

	if first.Token == second.Token {
		t.Fatal("routes must not share a token")
	}
	if status := post(t, second.URL, "Bearer "+first.Token); status != http.StatusUnauthorized {
		t.Fatalf("token of another route: status = %d, want 401", status)
	}
}

func TestHostUnknownRouteIsNotFound(t *testing.T) {
	host := NewHost()
	endpoint := mountTestServer(t, host, "a")
	t.Cleanup(func() { _ = host.Unmount("a") })

	unknown := strings.TrimSuffix(endpoint.URL, "/a") + "/nope"
	if status := post(t, unknown, "Bearer "+endpoint.Token); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestHostRejectsDuplicateSlug(t *testing.T) {
	host := NewHost()
	mountTestServer(t, host, "a")
	t.Cleanup(func() { _ = host.Unmount("a") })

	if _, err := host.Mount("a", testServer()); err == nil {
		t.Fatal("expected duplicate slug error")
	}
}

func TestHostStopsListeningAfterLastUnmount(t *testing.T) {
	host := NewHost()
	first := mountTestServer(t, host, "a")
	mountTestServer(t, host, "b")
	address := strings.TrimPrefix(strings.Split(first.URL, "/mcp/")[0], "http://")

	if err := host.Unmount("a"); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.Dial("tcp", address); err != nil {
		t.Fatalf("listener must stay up while a route remains: %v", err)
	} else {
		_ = conn.Close()
	}

	if err := host.Unmount("b"); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.Dial("tcp", address); err == nil {
		_ = conn.Close()
		t.Fatal("listener must be closed after the last unmount")
	}
}
