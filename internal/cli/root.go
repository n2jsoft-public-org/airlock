package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/kubeconfig"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/orgconfig"
	"github.com/n2jsoft/airlock/internal/selfupdate"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

// Execute is the public entrypoint for the CLI: the language is chosen before the commands are
// built, since their help is set at construction.
func Execute() error {
	path, err := appdir.ConfigFile()
	if err != nil {
		path = "" // loadConfig warns about it later.
	}
	i18n.SetLanguage(resolveLanguage(path, os.Getenv, i18n.OSLocale))
	return NewRootCmd().Execute()
}

// NewRootCmd builds the airlock command: without arguments in a terminal it opens the wizard.
func NewRootCmd() *cobra.Command {
	options := jumphost.Options{}

	cmd := &cobra.Command{
		Use:          "airlock [eks|rds|elasticache|dynamodb|s3|direct|docker] [name] [local-port]",
		Short:        i18n.T("cli.root.short"),
		Long:         i18n.T("cli.root.long"),
		Args:         cobra.MaximumNArgs(3),
		SilenceUsage: true,
		// main prints the returned error once.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// new copies options: the run fills in the arguments, the flags keep their values.
			return executeJumphost(cmd, args, new(options))
		},
	}

	cmd.Flags().StringVarP(&options.AwsProfile, "profile", "p", "", i18n.T("cli.root.flag.profile"))
	cmd.Flags().StringVar(&options.AwsProfile, "aws-profile", "", i18n.T("cli.root.flag.aws-profile"))
	cmd.Flags().StringVar(&options.AwsRegion, "region", "", i18n.T("cli.root.flag.region"))
	cmd.Flags().BoolVar(&options.SkipIDEDataSource, "no-ide-datasource", false, i18n.T("cli.root.flag.no-ide-datasource"))
	cmd.Flags().BoolVar(&options.SkipMCP, "no-mcp", false, i18n.T("cli.root.flag.no-mcp"))
	cmd.Flags().BoolVar(&options.MCPAllowWrites, "mcp-allow-writes", false, i18n.T("cli.root.flag.mcp-allow-writes"))
	cmd.Flags().StringVar(&options.DBUser, "db-user", "", i18n.T("cli.root.flag.db-user"))
	cmd.Flags().StringVar(&options.DBSecretARN, "db-secret", "", i18n.T("cli.root.flag.db-secret"))

	cmd.Flags().String("jumphost-tag", "", i18n.T("cli.root.flag.jumphost-tag"))
	cmd.Flags().String("read-only-secret-tag", "", i18n.T("cli.root.flag.read-only-secret-tag"))
	cmd.Flags().String("mcp-server-prefix", "", i18n.T("cli.root.flag.mcp-server-prefix"))
	cmd.Flags().String("mcp-clients", "", i18n.T("cli.root.flag.mcp-clients"))
	cmd.Flags().String("mode", "", i18n.T("cli.root.flag.mode"))
	registerCompletions(cmd)

	cmd.AddCommand(newConfigCmd(), newConnectionCmd(), newVersionCmd(), newUpdateCmd(), newUninstallCmd(), newDaemonCmd(), newSkillsCmd(), newMCPProxyCmd(), newEKSTokenCmd(), newRDSTokenCmd(), newSSOLoginCmd())
	localizeCobra(cmd)

	return cmd
}

// overridesFromFlags returns the config settings given as flags; flags left out are nil.
func overridesFromFlags(flags *pflag.FlagSet) config.Overrides {
	value := func(name string) *string {
		if !flags.Changed(name) {
			return nil
		}
		v, err := flags.GetString(name)
		if err != nil {
			return nil
		}
		return &v
	}
	return config.Overrides{
		JumphostTag:       value("jumphost-tag"),
		ReadOnlySecretTag: value("read-only-secret-tag"),
		MCPServerPrefix:   value("mcp-server-prefix"),
		MCPClients:        value("mcp-clients"),
		Mode:              value("mode"),
	}
}

// configSources are where the config of a command comes from: the config.json path and the
// organization resolved for it.
type configSources struct {
	path string
	org  orgState
}

// loadConfig is resolveConfig, with the organization notices printed on stderr.
func loadConfig(cmd *cobra.Command) (config.Config, configSources, error) {
	cfg, sources, err := resolveConfig(cmd)
	printOrganizationNotices(cmd, &sources)
	return cfg, sources, err
}

// resolveConfig merges the flags over the config file over the organization config, resolved
// once. A flag setting a key the organization locks is refused.
func resolveConfig(cmd *cobra.Command) (config.Config, configSources, error) {
	return resolveConfigWith(cmd, orgEnvironment())
}

// resolveConfigWith is resolveConfig, with the organization resolved in env.
func resolveConfigWith(cmd *cobra.Command, env orgEnv) (config.Config, configSources, error) {
	path, err := appdir.ConfigFile()
	if err != nil {
		cmd.PrintErr(i18n.Format("cli.root.warning_config_file_not", err))
		path = ""
	}
	sources := configSources{path: path, org: resolveOrganization(cmd.Context(), env, path)}
	if lockErr := lockedFlags(cmd.Flags(), sources.org.Org); lockErr != nil {
		return config.Config{}, sources, lockErr
	}
	cfg, err := config.Load(path, sources.org.Org)
	if err != nil {
		return config.Config{}, sources, err
	}
	if lockErr := lockedWritesFlag(cmd.Flags(), sources.org.Org, &cfg); lockErr != nil {
		return config.Config{}, sources, lockErr
	}
	cfg, err = cfg.Apply(overridesFromFlags(cmd.Flags()))
	return cfg, sources, err
}

func executeJumphost(cmd *cobra.Command, args []string, options *jumphost.Options) error {
	// A previous airlock update may leave the replaced binary behind (Windows); then the notice
	// comes from the last release check, refreshed in the background once a day.
	updates := defaultUpdateEnv()
	if target, err := updates.executable(); err == nil {
		if err = selfupdate.CleanupOld(target); err != nil {
			cmd.PrintErr(i18n.Format("cli.warning", err))
		}
	}
	latestRelease, staleCheck := updateNotice(updates, time.Now())
	if staleCheck {
		go func() {
			// Best effort: a failure only means the notice waits for a later launch.
			if err := refreshUpdateCheck(cmd.Context(), updates); err != nil {
				return
			}
		}()
	}
	interactive := isInteractiveInput(cmd.InOrStdin())
	// Without arguments, the dashboard opens right away on the wizard: one screen from start to end.
	wizard := len(args) == 0 && strings.TrimSpace(options.ResourceType) == "" && interactive
	cfg, sources, err := resolveConfig(cmd)
	// The wizard owns the terminal: it shows the notices itself. They are taken, and the diff
	// cleared, before the refresh starts: a clear saved after the refresh would undo it.
	var wizardOrg *wizardOrganization
	if err != nil || !wizard {
		printOrganizationNotices(cmd, &sources)
	} else {
		wizardOrg = takeWizardOrganization(&sources)
		// Every return from here on, an error before the dashboard included, ends the TUI.
		defer wizardOrg.printUnshown(cmd)
	}
	if err != nil {
		return err
	}
	if sources.org.Stale && sources.org.src.Kind != orgconfig.KindFile {
		go func() {
			// Best effort, like the release check: the next launch tells how the read went.
			if refreshErr := refreshOrganization(cmd.Context(), orgEnvironment(), sources.org.src); refreshErr != nil {
				return
			}
		}()
	}
	configPath, org := sources.path, sources.org.Org
	// Every opening registers its IDE data source and MCP servers in the directory airlock was
	// started in; empty leaves it to the runner (the working directory).
	projectDir, err := os.Getwd()
	if err != nil {
		projectDir = ""
	}

	// Runners share the registry so parallel sessions never reuse a local port or target, and an
	// MCP host per AI client selection so database sessions are served on one localhost listener
	// and registered in the same AI clients until mcpClients changes.
	registry := jumphost.NewSessionRegistry()
	hosts := newMCPHosts()
	// SQL run through MCP servers is logged to ~/.config/airlock/logs; a lookup failure only disables the log.
	mcpLogDir, err := appdir.LogsDir()
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), i18n.T("cli.root.warning_mcp_queries_will"), err) //nolint:errcheck // best-effort terminal output
	}
	// Each EKS session gets a kubeconfig in ~/.config/airlock/kube; a lookup failure only disables it.
	kubeconfigDir, err := appdir.KubeconfigDir()
	if err != nil {
		cmd.PrintErr(i18n.Format("cli.root.warning_eks_sessions_will", err))
	} else if _, removeErr := kubeconfig.RemoveOrphans(kubeconfigDir); removeErr != nil {
		// Kubeconfigs left by a killed airlock go; those of a running one stay.
		cmd.PrintErr(i18n.Format("cli.root.warning_stale_eks_kubeconfigs", removeErr))
	}
	// Connections live in ~/.config/airlock/saved-connections.json; a failure only disables the
	// remembered choices and the direct databases.
	var prefsPath string
	var databaseStore *connections.Databases
	if saved, openErr := openSavedConnections(cmd.ErrOrStderr()); openErr != nil {
		cmd.PrintErr(i18n.Format("cli.root.warning_saved_connections_are", openErr))
	} else {
		prefsPath, databaseStore = saved.Path(), saved.Databases()
	}
	shared := sharedRunnerOptions(&cfg, org, registry, hosts.For(&cfg), mcpLogDir, kubeconfigDir, databaseStore)
	shared.In = cmd.InOrStdin()
	databases := shared.Databases

	// SSO sign-ins suspend the dashboard, so their browser prompt, URL and code stay readable.
	handoff := ui.NewTerminalHandoff()
	// Passwords the user asks to remember go to the OS keychain; only the wizard offers it.
	keychainStore := newKeychain()
	// The dashboard's runner follows config.json, the flags of this run applied over it: the next
	// openings and the wizard use its new values. It stays silent: its progress would corrupt the
	// full-screen display.
	tuiRunners := newRunnerSource(configPath, org, withOverrides(overridesFromFlags(cmd.Flags()), func(next config.Config, nextOrg *config.Organization) (*jumphost.Runner, error) {
		// Not "options": that is the parameter of executeJumphost.
		tuiOptions := sharedRunnerOptions(&next, nextOrg, registry, hosts.For(&next), mcpLogDir, kubeconfigDir, databaseStore)
		tuiOptions.In = cmd.InOrStdin()
		tuiOptions.Out, tuiOptions.Err, tuiOptions.PrefsPath = io.Discard, io.Discard, prefsPath
		tuiOptions.Keychain, tuiOptions.Interactive = keychainStore, handoff.Run
		return jumphost.NewRunner(&tuiOptions), nil
	}))
	// Built once now, so the wizard always has a last valid runner to fall back on.
	if _, err = tuiRunners.Current(); err != nil {
		return err
	}
	discovery := reloadingDiscovery{source: tuiRunners}
	dashboard := ui.DashboardOptions{Handoff: handoff, Environments: cfg.EnvironmentSet(), WritesLocked: shared.WritesLocked}
	if wizardOrg != nil {
		dashboard.OrganizationNotice, dashboard.OrganizationNoticeShown = wizardOrg.banner(), wizardOrg.markShown
	}
	if configPath != "" {
		store := newConfigSettingsStore(configPath, org, cmd.Flags())
		store.managed = sources.org.Managed
		dashboard.Settings = store
	}
	if databaseStore != nil {
		dashboard.Databases = databaseStore
	}
	dashboard.Docker = shared.Containers
	if latestRelease != "" {
		dashboard.UpdateNotice = wizardUpdateNotice(latestRelease)
	}
	// The dashboard's sessions live in this process and end with it (CLI mode).
	manager := sessions.NewManager(cmd.Context(), sessions.Config{Runner: tuiRunners.Source()})
	dashboard.ProjectDir = projectDir

	if wizard {
		if shouldUseDaemon(&cfg, interactive) {
			return runDaemonView(cmd, options, false, discovery, &dashboard)
		}
		return ui.RunJumphostSessions(cmd.Context(), manager, discovery, 0, *options, cmd.OutOrStdout(), dashboard)
	}

	if latestRelease != "" {
		cmd.PrintErrln(stderrUpdateNotice(latestRelease))
	}

	if len(args) > 0 {
		options.ResourceType = strings.ToLower(strings.TrimSpace(args[0]))
	}
	if module := jumphost.ModuleOf(options.ResourceType); module != "" && slices.Contains(cfg.DisabledModules, module) {
		return i18n.NewError("jumphost.modules.disabled", module, options.ResourceType)
	}
	if options.ResourceType == directResourceType {
		if options.Name, err = checkDirectTarget(cmd, args, databases); err != nil {
			return err
		}
		return openDirect(cmd, options, interactive, &directEnv{
			discovery: discovery, manager: manager, dashboard: &dashboard, shared: &shared, cfg: &cfg, prefsPath: prefsPath,
		})
	}
	if options.ResourceType == dockerResourceType {
		if options.Name, err = checkDockerTarget(cmd, args, shared.Containers, interactive); err != nil {
			return err
		}
		return openDocker(cmd, options, interactive, &directEnv{
			discovery: discovery, manager: manager, dashboard: &dashboard, shared: &shared, cfg: &cfg, prefsPath: prefsPath,
		})
	}
	if len(args) > 1 {
		options.Name = strings.TrimSpace(args[1])
	}
	if len(args) > 2 {
		localPort, convErr := strconv.Atoi(args[2])
		if convErr != nil {
			return i18n.NewError("cli.root.invalid_local_port", args[2], convErr)
		}
		if localPort <= 0 {
			return i18n.NewError("cli.root.invalid_local_port_must", args[2])
		}
		options.LocalPortNumber = localPort
	}
	// In daemon mode, an interactive run is a view of the daemon, which opens the named target.
	if shouldUseDaemon(&cfg, interactive) {
		openFirst := options.ResourceType != "" && options.Name != ""
		return runDaemonView(cmd, options, openFirst, discovery, &dashboard)
	}

	if options.ResourceType == "" {
		resourceType, promptErr := promptResourceType(cmd.InOrStdin(), cmd.OutOrStdout())
		if promptErr != nil {
			return promptErr
		}
		options.ResourceType = resourceType
	}

	runnerOptions := shared
	runnerOptions.Out, runnerOptions.Err = cmd.OutOrStdout(), cmd.ErrOrStderr()
	runner := jumphost.NewRunner(&runnerOptions)

	if !interactive {
		return runner.Run(cmd.Context(), options)
	}

	session, err := runner.Prepare(cmd.Context(), *options)
	if err != nil {
		return err
	}
	first := manager.Adopt(session, options)

	return ui.RunJumphostSessions(cmd.Context(), manager, discovery, first, *options, cmd.OutOrStdout(), dashboard)
}

// sharedRunnerOptions are the runner options of both modes, from the config, its organization and
// the directories airlock found; a nil databaseStore disables the direct databases.
func sharedRunnerOptions(cfg *config.Config, org *config.Organization, registry *jumphost.SessionRegistry, mcpHost *mcpserve.Host, mcpLogDir, kubeconfigDir string, databaseStore *connections.Databases) jumphost.RunnerOptions {
	// A nil store must stay a nil interface: the runner and the dashboard test for nil.
	var databases jumphost.DirectDatabases
	if databaseStore != nil {
		databases = databaseStore
	}
	return jumphost.RunnerOptions{
		Registry:          registry,
		MCPHost:           mcpHost,
		MCPLogDir:         mcpLogDir,
		DefaultRegion:     cfg.DefaultRegion,
		DefaultDBUser:     cfg.DefaultDBUser,
		JumphostTag:       cfg.JumphostTag,
		ReadOnlySecretTag: cfg.ReadOnlySecretTag,
		MCPServerPrefix:   cfg.MCPServerPrefix,
		KubeconfigDir:     kubeconfigDir,
		Databases:         databases,
		Containers:        dockerContainers(),
		Environments:      cfg.EnvironmentSet(),
		RandomLocalPort:   cfg.RandomLocalPort,
		DisabledModules:   cfg.DisabledModules,
		WritesLocked:      writesLocked(org, cfg),
	}
}

func promptResourceType(in io.Reader, out io.Writer) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", i18n.NewError("cli.root.resource_type_is_required")
	}

	_, _ = fmt.Fprint(out, i18n.T("cli.root.please_specify_resource_type")) //nolint:errcheck // best-effort terminal output
	var value string
	_, err := fmt.Fscanln(in, &value)
	if err != nil {
		return "", i18n.NewError("cli.root.failed_to_read_resource", err)
	}

	value = strings.ToLower(strings.TrimSpace(value))
	if value != "eks" && value != "rds" && value != "elasticache" && value != "dynamodb" && value != "s3" {
		return "", i18n.NewError("cli.root.invalid_resource_type_expected", value)
	}

	return value, nil
}

func isInteractiveInput(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}

	return term.IsTerminal(int(file.Fd()))
}
