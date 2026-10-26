package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/aiclient"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// TokenFileName is the file of airlock's config directory holding the gateway's bearer token.
const TokenFileName = "gateway-token"

// tokenBytes is the size of the token, written as hex.
const tokenBytes = 32

// URL is where the gateway listening on port is reached.
func URL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
}

// loadToken returns the token kept in dir, writing a new one when the file is missing, unreadable
// or malformed; created tells that it did. The token stays the same across daemon restarts, so
// the entry the AI clients loaded stays valid.
func loadToken(dir string) (token string, created bool, err error) {
	path := filepath.Join(dir, TokenFileName)
	if raw, readErr := os.ReadFile(path); readErr == nil { // #nosec G304 -- airlock's own config file.
		if token = strings.TrimSpace(string(raw)); validToken(token) {
			return token, false, nil
		}
	}
	buf := make([]byte, tokenBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("failed to generate the gateway token: %w", err)
	}
	token = hex.EncodeToString(buf)
	if writeErr := writePrivate(path, token+"\n"); writeErr != nil {
		return "", false, writeErr
	}
	return token, true, nil
}

func validToken(s string) bool {
	if len(s) != 2*tokenBytes || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// writePrivate replaces path with content, readable by the user only.
func writePrivate(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	_, writeErr := tmp.WriteString(content)
	closeErr := tmp.Close()
	if err = errors.Join(writeErr, closeErr, os.Chmod(tmp.Name(), 0o600)); err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // the write failed already.
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// registration is the gateway entry in the user-level files of the AI clients.
type registration struct {
	entry   mcpserve.ClientEntry
	written []string
	err     error
	clients aiclient.Selection
}

// register writes entry in the user-level targets of clients: the daemon runs in no project.
func register(clients aiclient.Selection, entry mcpserve.ClientEntry) *registration {
	r := &registration{clients: clients.UserOnly(), entry: entry}
	r.written, r.err = mcpserve.RegisterClients("", r.clients, entry)
	return r
}

// remove deletes the entry from the files, unless another gateway replaced it.
func (r *registration) remove() error {
	_, err := mcpserve.UnregisterClients("", r.clients, r.entry)
	return err
}

// status describes the gateway for `airlock daemon status`; the token is shown when no client
// file holds it.
func (r *registration) status() string {
	return r.describe("; header Authorization: Bearer " + r.entry.Token)
}

// statusWithoutToken is status without the token, for daemon.log.
func (r *registration) statusWithoutToken() string { return r.describe("") }

// describe is the status, ending with how to authenticate when no client file holds the entry.
func (r *registration) describe(auth string) string {
	status := "on " + r.entry.URL
	if len(r.written) > 0 {
		status += ", registered in " + mcpserve.RelativeTo("", r.written)
	}
	switch {
	case r.err != nil:
		return fmt.Sprintf("%s, registration failed: %v%s", status, r.err, auth)
	case len(r.written) == 0:
		return status + ", not registered (no :user client in mcp.clients)" + auth
	default:
		return status
	}
}
