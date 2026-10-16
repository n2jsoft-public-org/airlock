package cli

import (
	"context"
	"sync"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/ui"
)

// withOverrides applies the flags of this run over every configuration build gets: a flag keeps
// winning over config.json when the file changes, but not over a key the organization now locks.
func withOverrides[R any](overrides config.Overrides, build func(config.Config, *config.Organization) (R, error)) func(config.Config, *config.Organization) (R, error) {
	return func(file config.Config, org *config.Organization) (R, error) {
		applied, err := file.Apply(unlockedOverrides(overrides, org))
		if err != nil {
			var none R
			return none, err
		}
		return build(applied, org)
	}
}

// mcpHosts keeps one MCP host per value of mcpClients: sessions share one localhost listener
// until the setting changes, then the next ones are registered in the new AI clients.
type mcpHosts struct {
	byClients map[string]*mcpserve.Host
	mu        sync.Mutex
}

func newMCPHosts() *mcpHosts {
	return &mcpHosts{byClients: map[string]*mcpserve.Host{}}
}

// For returns the host of the AI clients of cfg.
func (h *mcpHosts) For(cfg *config.Config) *mcpserve.Host {
	h.mu.Lock()
	defer h.mu.Unlock()
	host, ok := h.byClients[cfg.MCPClients]
	if !ok {
		host = mcpserve.NewHostFor(cfg.MCPClientSelection())
		h.byClients[cfg.MCPClients] = host
	}
	return host
}

// reloadingDiscovery is the wizard discovery of the runner of the next opening: like the
// openings, it follows config.json. An invalid file keeps the last valid runner, as the
// resolvers have no error to report.
type reloadingDiscovery struct {
	source *runnerSource[*jumphost.Runner]
}

var _ ui.JumphostDiscovery = reloadingDiscovery{}

// runner is the runner of the next opening, else the last valid one. The source is built once
// before the dashboard opens, so Last always has one.
func (d reloadingDiscovery) runner() *jumphost.Runner {
	if runner, err := d.source.Current(); err == nil {
		return runner
	}
	runner, _ := d.source.Last()
	return runner
}

func (d reloadingDiscovery) ListAWSProfileSummaries(ctx context.Context) ([]jumphost.AWSProfileSummary, error) {
	return d.runner().ListAWSProfileSummaries(ctx)
}

func (d reloadingDiscovery) ListResourceSummaries(ctx context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, error) {
	return d.runner().ListResourceSummaries(ctx, options)
}

func (d reloadingDiscovery) ResolveDefaultLocalPort(ctx context.Context, options jumphost.Options) (int, error) { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	return d.runner().ResolveDefaultLocalPort(ctx, options)
}

func (d reloadingDiscovery) CheckLocalPort(port int) error {
	return d.runner().CheckLocalPort(port)
}

func (d reloadingDiscovery) ResolveRegion(ctx context.Context, profile string) string {
	return d.runner().ResolveRegion(ctx, profile)
}

func (d reloadingDiscovery) ResolveDBUser(engine, user string) string {
	return d.runner().ResolveDBUser(engine, user)
}

func (d reloadingDiscovery) LoadConnectionPrefs(ctx context.Context, options jumphost.Options) (jumphost.ConnectionPrefs, bool) { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	return d.runner().LoadConnectionPrefs(ctx, options)
}

func (d reloadingDiscovery) SaveConnectionPrefs(ctx context.Context, options jumphost.Options) error { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	return d.runner().SaveConnectionPrefs(ctx, options)
}

func (d reloadingDiscovery) ListSavedConnections(ctx context.Context, profile, region string) ([]jumphost.SavedConnection, error) {
	return d.runner().ListSavedConnections(ctx, profile, region)
}

func (d reloadingDiscovery) ForgetConnection(ctx context.Context, ref jumphost.TargetRef) error {
	return d.runner().ForgetConnection(ctx, ref)
}

func (d reloadingDiscovery) ModuleDisabled(resourceType string) bool {
	return d.runner().ModuleDisabled(resourceType)
}

func (d reloadingDiscovery) KeychainAvailable() bool {
	return d.runner().KeychainAvailable()
}

func (d reloadingDiscovery) LoadKeychainPassword(ctx context.Context, options jumphost.Options) (string, bool) { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	return d.runner().LoadKeychainPassword(ctx, options)
}

func (d reloadingDiscovery) ListDBSecrets(ctx context.Context, options *jumphost.Options) ([]jumphost.SecretSummary, error) {
	return d.runner().ListDBSecrets(ctx, options)
}

func (d reloadingDiscovery) ListCacheIAMUsers(ctx context.Context, options *jumphost.Options) ([]string, error) {
	return d.runner().ListCacheIAMUsers(ctx, options)
}

func (d reloadingDiscovery) SearchResources(ctx context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, map[string]error, error) {
	return d.runner().SearchResources(ctx, options)
}

func (d reloadingDiscovery) FindResource(ctx context.Context, options *jumphost.Options, account string) (jumphost.ResourceSummary, error) {
	return d.runner().FindResource(ctx, options, account)
}
