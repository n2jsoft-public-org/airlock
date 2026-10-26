package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/appdir"
	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/keychain"
	"github.com/n2jsoft-public-org/airlock/internal/orgconfig"
	"github.com/n2jsoft-public-org/airlock/internal/selfupdate"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// organizationTokenAccount is the keychain account of the GitHub token reading the organization
// config, when neither the environment nor gh gives one.
const organizationTokenAccount = "organization/github.com"

// firstReadTimeout bounds the synchronous read of a remote source without a cache.
const firstReadTimeout = 5 * time.Second

// refreshTimeout bounds the background refresh of a remote source.
const refreshTimeout = 10 * time.Second

// notRefreshedNoticeAge is the age of the last successful read the launch mentions.
const notRefreshedNoticeAge = 24 * time.Hour

// orgEnv is everything resolveOrganization reads from the machine; an empty managedPath is no
// managed file.
type orgEnv struct {
	fetcher                *orgconfig.Fetcher
	now                    func() time.Time
	readFile               func(string) ([]byte, error)
	managedPath, cachePath string
	// cacheOnly never reads a remote source: without its cache, there is no organization.
	cacheOnly bool
}

// defaultOrgEnv is the orgEnv of this machine. A config directory airlock cannot find leaves the
// cache path empty: remote sources then fail open.
func defaultOrgEnv() orgEnv {
	cachePath, err := appdir.OrganizationCacheFile()
	if err != nil {
		cachePath = ""
	}
	return orgEnv{
		managedPath: appdir.ManagedFile(runtime.GOOS),
		cachePath:   cachePath,
		fetcher:     &orgconfig.Fetcher{Token: organizationToken(newKeychain())},
		now:         time.Now,
		readFile:    os.ReadFile,
	}
}

// orgEnvironment is the orgEnv the commands resolve the organization with; tests replace it.
var orgEnvironment = defaultOrgEnv

// orgState is the organization config resolved for a command, with what to tell the user about it.
type orgState struct {
	FetchedAt time.Time
	Org       *config.Organization // nil: no organization layer
	Source    string
	Warnings  []string
	Changes   []config.Change  // since the body the user last saw
	src       orgconfig.Source // the parsed source, empty for inline managed settings; never printed
	Managed   bool             // the source comes from the managed file
	Stale     bool             // the cache is due for a refresh
}

// organizationFor resolves the organization of the config.json at localPath.
func organizationFor(ctx context.Context, localPath string) orgState {
	return resolveOrganization(ctx, orgEnvironment(), localPath)
}

// resolveOrganization finds the organization source (the managed file, else organizationConfig of
// localPath) and reads it. It fails open: every problem is a warning and no organization.
// Warnings and Source name the source in its printable form: a URL query may hold a secret.
func resolveOrganization(ctx context.Context, env orgEnv, localPath string) orgState {
	var state orgState
	managed, err := readManaged(env)
	if err != nil {
		// An invalid managed file is ignored, as if absent.
		state = state.warn(i18n.Format("cli.organization.managed_invalid", env.managedPath, err))
	}
	raw := ""
	switch {
	case managed != nil && managed.Inline != nil:
		state.Managed, state.Source = true, env.managedPath
		return state.apply(managed.Inline)
	case managed != nil:
		state.Managed, raw = true, managed.Source
	default:
		// organizationConfig is personal: no organization changes it. An invalid file fails the
		// command later, as it always has.
		local, loadErr := config.Load(localPath, nil)
		if loadErr != nil {
			return state
		}
		raw = local.OrganizationConfig
	}
	if raw == "" {
		return state
	}
	src, err := orgconfig.ParseSource(raw)
	if err != nil {
		// The error names the source in its printable form.
		return state.warn(i18n.Format("cli.organization.invalid_source", err))
	}
	state.src, state.Source = src, src.Display()
	if src.Kind == orgconfig.KindFile {
		got, fetchErr := env.fetcher.Fetch(ctx, src, "")
		if fetchErr != nil {
			return state.warn(organizationWarning(state.Source, fetchErr))
		}
		return state.apply(got.Body)
	}

	now := env.now()
	cache, cached := orgconfig.LoadCache(env.cachePath).For(src.Raw)
	switch {
	case cached:
		state.Stale = cache.Stale(now)
	case env.cacheOnly:
		return state
	default:
		readCtx, cancel := context.WithTimeout(ctx, firstReadTimeout)
		_, err = orgconfig.Refresh(readCtx, env.fetcher, src, env.cachePath, now, validOrganization(state.Source))
		cancel()
		if err != nil {
			return state.warn(organizationWarning(state.Source, err))
		}
		if cache, cached = orgconfig.LoadCache(env.cachePath).For(src.Raw); !cached {
			return state
		}
	}
	state.FetchedAt = cache.FetchedAt
	if cache.Rejected != "" {
		state = state.warn(i18n.Format("cli.organization.rejected", state.Source, cache.Rejected))
	}
	state = state.apply(cache.Body)
	if len(cache.Previous) > 0 && state.Org != nil {
		if previous, _, parseErr := config.ParseOrganization(state.Source, cache.Previous); parseErr == nil {
			state.Changes = config.DiffOrganizations(previous, state.Org)
		}
	}
	return state
}

// validOrganization rejects a body that is not a valid organization config of source.
func validOrganization(source string) func([]byte) error {
	return func(body []byte) error {
		_, _, err := config.ParseOrganization(source, body)
		return err
	}
}

// refreshOrganization fetches src into the cache for the next launch; an invalid body leaves the
// cache as it was.
func refreshOrganization(ctx context.Context, env orgEnv, src orgconfig.Source) error { //nolint:gocritic // Source by value is the orgconfig contract.
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	_, err := orgconfig.Refresh(ctx, env.fetcher, src, env.cachePath, env.now(), validOrganization(src.Display()))
	return err
}

// lockableFlags are the root flags setting a stored key, with their field of config.Overrides.
// --region and --db-user are per-run choices a lock leaves free.
var lockableFlags = []struct {
	field     func(*config.Overrides) **string
	flag, key string
}{
	{func(o *config.Overrides) **string { return &o.JumphostTag }, "jumphost-tag", "aws.jumphostTag"},
	{func(o *config.Overrides) **string { return &o.ReadOnlySecretTag }, "read-only-secret-tag", "modules.sql.readOnlySecretTag"},
	{func(o *config.Overrides) **string { return &o.MCPServerPrefix }, "mcp-server-prefix", "mcp.serverPrefix"},
	{func(o *config.Overrides) **string { return &o.MCPClients }, "mcp-clients", "mcp.clients"},
	{func(o *config.Overrides) **string { return &o.Mode }, "mode", "mode"},
}

// lockedFlags refuses the flags of this run setting a key org locks.
func lockedFlags(flags *pflag.FlagSet, org *config.Organization) error {
	for _, f := range lockableFlags {
		if flags.Changed(f.flag) && org.Locked(f.key) {
			return lockedFlagError{i18n.NewError("cli.organization.flag_locked", f.flag, f.key, org.Source)}
		}
	}
	return nil
}

// lockedWritesFlag refuses --mcp-allow-writes when the organization locks MCP writes off.
func lockedWritesFlag(flags *pflag.FlagSet, org *config.Organization, cfg *config.Config) error {
	if flags.Changed("mcp-allow-writes") && writesLocked(org, cfg) {
		return lockedFlagError{i18n.NewError("cli.organization.flag_locked", "mcp-allow-writes", "agent.writes", org.Source)}
	}
	return nil
}

// writesLocked reports whether org locks MCP writes off. A locked agent.writes true only fixes
// the agent setting: it never turns writes on for the sessions the user opens.
func writesLocked(org *config.Organization, cfg *config.Config) bool {
	return org.Locked("agent.writes") && !cfg.AgentWrites
}

// unlockedOverrides is o without the flags setting a key org locks: a lock the organization adds
// while airlock runs wins over the flags of the run.
func unlockedOverrides(o config.Overrides, org *config.Organization) config.Overrides {
	for _, f := range lockableFlags {
		if org.Locked(f.key) {
			*f.field(&o) = nil
		}
	}
	return o
}

// lockedFlagError is the localized refusal of a flag; errors.Is finds config.ErrLocked in it.
type lockedFlagError struct{ error }

func (e lockedFlagError) Unwrap() []error    { return []error{e.error} }
func (lockedFlagError) Is(target error) bool { return target == config.ErrLocked }

// organizationNotices are what the user must know about the organization config of state:
// warnings, an old last read, local values a lock ignores, local copies of its values.
func organizationNotices(state orgState, settings []config.Setting, now time.Time) []string { //nolint:gocritic // orgState is a value the resolver builds.
	notices := slices.Clone(state.Warnings)
	if !state.FetchedAt.IsZero() && now.Sub(state.FetchedAt) > notRefreshedNoticeAge {
		notices = append(notices, i18n.Format("cli.organization.not_refreshed", state.Source, state.FetchedAt.Local().Format(time.DateTime)))
	}
	equal := 0
	for idx := range settings {
		s := &settings[idx]
		switch {
		case s.Locked && s.Ignored != "":
			notices = append(notices, i18n.Format("cli.organization.ignored_local", s.Name, s.Ignored))
		case s.RepeatsOrganization(state.Org):
			equal++
		}
	}
	if equal > 0 {
		notices = append(notices, i18n.Plural(equal, "cli.organization.shadowed"))
	}
	return notices
}

// organizationDiff renders the last changes of the organization config like the config import
// plan; nil when there are none.
func organizationDiff(state *orgState) []string {
	if len(state.Changes) == 0 {
		return nil
	}
	return append([]string{i18n.Format("cli.organization.changed", state.Source)}, planLines(state.Changes)...)
}

// printOrganizationNotices prints the organization notices of sources on stderr, and its last
// changes once.
func printOrganizationNotices(cmd *cobra.Command, sources *configSources) {
	notices, diff := takeOrganizationNotices(sources)
	printOrganizationLines(cmd, notices, diff)
}

func printOrganizationLines(cmd *cobra.Command, notices, diff []string) {
	for _, notice := range notices {
		cmd.PrintErr(i18n.Format("cli.warning", notice))
	}
	for _, line := range diff {
		cmd.PrintErrln(line)
	}
}

// wizardOrganization is what printOrganizationNotices prints, for the wizard banner. Their diff is
// cleared already: unless the wizard showed them in full, they are printed when airlock exits.
type wizardOrganization struct {
	notices, diff []string
	shown         bool
}

func takeWizardOrganization(sources *configSources) *wizardOrganization {
	notices, diff := takeOrganizationNotices(sources)
	return &wizardOrganization{notices: notices, diff: diff}
}

// banner is the lines of the wizard banner.
func (w *wizardOrganization) banner() []string {
	return append(slices.Clone(w.notices), w.diff...)
}

// markShown records that the wizard showed the banner in full.
func (w *wizardOrganization) markShown() { w.shown = true }

// printUnshown prints the notices the wizard did not show in full, once the terminal is back.
func (w *wizardOrganization) printUnshown(cmd *cobra.Command) {
	if !w.shown {
		printOrganizationLines(cmd, w.notices, w.diff)
	}
}

// takeOrganizationNotices returns the organization notices of sources and its last changes, which
// it forgets: they show once.
func takeOrganizationNotices(sources *configSources) (notices, diff []string) {
	settings, err := config.Settings(sources.path, sources.org.Org)
	if err != nil {
		settings = nil // The command reports an invalid config.json.
	}
	env := orgEnvironment()
	notices = organizationNotices(sources.org, settings, env.now())
	diff = organizationDiff(&sources.org)
	if diff == nil {
		return notices, nil
	}
	if err = clearOrganizationDiff(env, sources.org.src); err != nil {
		notices = append(notices, err.Error())
	}
	return notices, diff
}

// clearOrganizationDiff forgets the previous body of the cache of src, once its diff was shown.
func clearOrganizationDiff(env orgEnv, src orgconfig.Source) error { //nolint:gocritic // Source by value is the orgconfig contract.
	cache, ok := orgconfig.LoadCache(env.cachePath).For(src.Raw)
	if !ok {
		return nil
	}
	cache.Previous = nil
	return orgconfig.SaveCache(env.cachePath, cache)
}

// readManaged returns the managed config of env, nil when there is none.
func readManaged(env orgEnv) (*config.Managed, error) {
	if env.managedPath == "" {
		return nil, nil
	}
	data, err := env.readFile(env.managedPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	managed, err := config.ParseManaged(data)
	if err != nil {
		return nil, err
	}
	return &managed, nil
}

// apply parses body as the organization of s.Source; an invalid one is a warning.
func (s orgState) apply(body []byte) orgState { //nolint:gocritic // orgState is a value the resolver builds.
	org, warnings, err := config.ParseOrganization(s.Source, body)
	if err != nil {
		return s.warn(organizationWarning(s.Source, err))
	}
	s.Org, s.Warnings = org, append(s.Warnings, warnings...)
	return s
}

func (s orgState) warn(warning string) orgState { //nolint:gocritic // orgState is a value the resolver builds.
	s.Warnings = append(s.Warnings, warning)
	return s
}

// organizationWarning tells why the organization config of source is not applied, and how to fix it.
func organizationWarning(source string, err error) string {
	switch {
	case errors.Is(err, orgconfig.ErrAccess):
		return i18n.Format("cli.organization.access", source)
	case errors.Is(err, fs.ErrNotExist):
		return i18n.Format("cli.organization.not_found", source)
	case errors.Is(err, orgconfig.ErrUnreachable):
		return i18n.Format("cli.organization.unreachable", source, err)
	default:
		return i18n.Format("cli.organization.not_applied", source, err)
	}
}

// organizationToken returns the GitHub token reading the organization config: the one of
// airlock update (GH_TOKEN, GITHUB_TOKEN, gh), else the one stored in the keychain.
func organizationToken(store keychain.Store) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if token, err := selfupdate.Token(ctx); err == nil {
			return token, nil
		}
		return store.Get(organizationTokenAccount)
	}
}
