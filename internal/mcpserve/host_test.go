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

func post(t *testing.T, url, authorization string) int {
	t.Helper()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err = response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode
}

// unmountOnCleanup unmounts slugs when the test ends.
func unmountOnCleanup(t *testing.T, host *Host, slugs ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, slug := range slugs {
			if err := host.Unmount(slug); err != nil {
				t.Errorf("unmount %s: %v", slug, err)
			}
		}
	})
}

func TestHostServesMountedServerToAuthorizedClient(t *testing.T) {
	host := NewHost()
	endpoint := mountTestServer(t, host, "acme-dev-app-abc123")
	unmountOnCleanup(t, host, "acme-dev-app-abc123")

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
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("closing the client session: %v", closeErr)
		}
	}()
	if names := toolNames(t, session); len(names) != 1 {
		t.Fatalf("tools = %v", names)
	}
}

func TestHostRejectsMissingOrWrongToken(t *testing.T) {
	host := NewHost()
	endpoint := mountTestServer(t, host, "a")
	unmountOnCleanup(t, host, "a")

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
	unmountOnCleanup(t, host, "a", "b")

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
	unmountOnCleanup(t, host, "a")

	unknown := strings.TrimSuffix(endpoint.URL, "/a") + "/nope"
	if status := post(t, unknown, "Bearer "+endpoint.Token); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestHostRejectsDuplicateSlug(t *testing.T) {
	host := NewHost()
	mountTestServer(t, host, "a")
	unmountOnCleanup(t, host, "a")

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
	} else if err = conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err := host.Unmount("b"); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.Dial("tcp", address); err == nil {
		_ = conn.Close() //nolint:errcheck // the test fails either way.
		t.Fatal("listener must be closed after the last unmount")
	}
}

func TestHostUnmountClosesTheSessionsOfTheRoute(t *testing.T) {
	host := NewHost()
	server := testServer()
	endpoint, err := host.Mount("a", server)
	if err != nil {
		t.Fatal(err)
	}
	// Another route keeps the listener up: only the route's sessions must end.
	mountTestServer(t, host, "b")
	unmountOnCleanup(t, host, "b")
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	// Before 2026-07-28 the client sends no server/discover probe: its POST runs on a server session
	// the SDK closes only after answering, which the count below could see.
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{token: endpoint.Token}, Timeout: 10 * time.Second},
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }() //nolint:errcheck // the server closed it already.
	count := func() int {
		n := 0
		for range server.Sessions() {
			n++
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("sessions before unmount = %d, want 1", n)
	}

	if err := host.Unmount("a"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("sessions after unmount = %d, want 0", n)
	}
}
