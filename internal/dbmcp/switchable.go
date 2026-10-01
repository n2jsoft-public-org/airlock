package dbmcp

import (
	"context"
	"sync"
)

// switchableProvider is the credential provider of a running MCP server, replaced when the
// dashboard switches the session to other credentials.
type switchableProvider struct {
	current CredentialProvider
	mu      sync.Mutex
}

func (p *switchableProvider) get() CredentialProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

func (p *switchableProvider) set(creds CredentialProvider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = creds
}

func (p *switchableProvider) Credentials(ctx context.Context, database string) (Credentials, error) {
	return p.get().Credentials(ctx, database)
}

func (p *switchableProvider) Databases(ctx context.Context) ([]string, error) {
	return p.get().Databases(ctx)
}

// unwrapProvider returns the provider doing the work, behind a switchableProvider: its optional
// behaviors (refresh, rejection, hint) are those of the current provider.
func unwrapProvider(creds CredentialProvider) CredentialProvider {
	if switchable, ok := creds.(*switchableProvider); ok {
		return switchable.get()
	}
	return creds
}
