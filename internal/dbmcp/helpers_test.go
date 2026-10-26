package dbmcp

import (
	"net/http"
	"os"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// bearerTransport adds an Authorization header to every request.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func useIsolatedLocks(t *testing.T) {
	t.Helper()
	t.Cleanup(mcpserve.IsolateLocks(t.TempDir()))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
