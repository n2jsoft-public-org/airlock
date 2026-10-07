package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/kubeconfig"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/selfupdate"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

// Execute is the public entrypoint for the CLI.
func Execute() error {
	return NewRootCmd().Execute()
}

// NewRootCmd builds the airlock command: without arguments in a terminal it opens the wizard.
func NewRootCmd() *cobra.Command {
	options := jumphost.Options{}

	cmd := &cobra.Command{
		Use:   "airlock [eks|rds|elasticache|direct] [name] [local-port]",
		Short: "Connect to EKS, RDS, or ElastiCache through an SSM jumphost, or to a direct database",
		Long: "Connects to a private AWS resource (EKS, RDS, ElastiCache) through SSM Session Manager port forwarding, " +
			"or to a direct database declared with `airlock connection add` (`airlock direct <name>`). " +
			"When values are omitted in an interactive shell, a wizard asks for them.",
		Args:         cobra.MaximumNArgs(3),
		SilenceUsage: true,
		// main prints the returned error once.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// new copies options: the run fills in the arguments, the flags keep their values.
			return executeJumphost(cmd, args, new(options))
		},
	}

	cmd.Flags().StringVarP(&options.AwsProfile, "profile", "p", "", "AWS profile to use (the wizard asks when omitted)")
	cmd.Flags().StringVar(&options.AwsProfile, "aws-profile", "", "Alias of --profile")
	cmd.Flags().StringVar(&options.AwsRegion, "region", "", "AWS region to use (defaults to AWS_REGION, then the profile region)")
	cmd.Flags().BoolVar(&options.SkipIDEDataSource, "no-ide-datasource", false, "Do not register the connection in ./.idea/dataSources.xml")
	cmd.Flags().BoolVar(&options.SkipMCP, "no-mcp", false, "Do not serve an MCP server giving AI assistants access to the database, cluster or cache")
	cmd.Flags().BoolVar(&options.MCPAllowWrites, "mcp-allow-writes", false, "Let the MCP server modify data of databases and caches (EKS stays read-only)")
	cmd.Flags().StringVar(&options.DBUser, "db-user", "", "Database or cache user signing in with AWS IAM (RDS: defaults to the engine's user in \"defaultDbUser\" of the config file)")
	cmd.Flags().StringVar(&options.DBSecretARN, "db-secret", "", "Secrets Manager secret (ARN or name) holding the database or cache password, as JSON or plain text")

	cmd.Flags().String("jumphost-tag", "", `Tag of the SSM jumphost instance, as Key=Value (config "jumphostTag", default Usage=jumphost)`)
	cmd.Flags().String("read-only-secret-tag", "", `Tag of the read-only SQL Server secrets, as Key=Value (config "readOnlySecretTag")`)
	cmd.Flags().String("mcp-server-prefix", "", `Prefix of the MCP server names (config "mcpServerPrefix", default airlock)`)
	cmd.Flags().String("mcp-clients", "", `AI clients the MCP servers are registered in: auto, none, or e.g. claude:user,codex (config "mcpClients", default auto)`)
	cmd.Flags().String("mode", "", `cli: sessions close with airlock; daemon: a background daemon keeps them (config "mode", default cli)`)
	registerCompletions(cmd)

	cmd.AddCommand(newConfigCmd(), newConnectionCmd(), newVersionCmd(), newUpdateCmd(), newUninstallCmd(), newDaemonCmd(), newMCPProxyCmd())

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

// loadConfig merges the flags over the config file; it also returns the file path.
func loadConfig(cmd *cobra.Command) (config.Config, string, error) {
	path, err := appdir.ConfigFile()
	if err != nil {
		cmd.PrintErrf("Warning: config file not read: %v\n", err)
		path = ""
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, path, err
	}
	cfg, err = cfg.Apply(overridesFromFlags(cmd.Flags()))
	return cfg, path, err
}

func executeJumphost(cmd *cobra.Command, args []string, options *jumphost.Options) error {
	// A previous airlock update may leave the replaced binary behind (Windows); then the notice
	// comes from the last release check, refreshed in the background once a day.
	updates := defaultUpdateEnv()
	if target, err := updates.executable(); err == nil {
		if err = selfupdate.CleanupOld(target); err != nil {
			cmd.PrintErrf("Warning: %v\n", err)
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
	cfg, configPath, err := loadConfig(cmd)
	if err != nil {
		return err
	}
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
	interactive := isInteractiveInput(cmd.InOrStdin())
	// SQL run through MCP servers is logged to ~/.config/airlock/logs; a lookup failure only disables the log.
	mcpLogDir, err := appdir.LogsDir()
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: MCP queries will not be logged: %v\n", err) //nolint:errcheck // best-effort terminal output
	}
	// Each EKS session gets a kubeconfig in ~/.config/airlock/kube; a lookup failure only disables it.
	kubeconfigDir, err := appdir.KubeconfigDir()
	if err != nil {
		cmd.PrintErrf("Warning: EKS sessions will not get a kubeconfig: %v\n", err)
	} else if _, removeErr := kubeconfig.RemoveOrphans(kubeconfigDir); removeErr != nil {
		// Kubeconfigs left by a killed airlock go; those of a running one stay.
		cmd.PrintErrf("Warning: stale EKS kubeconfigs were not all removed: %v\n", removeErr)
	}
	// Connections live in ~/.config/airlock/saved-connections.json; a failure only disables the
	// remembered choices and the direct databases.
	var prefsPath string
	var databaseStore *connections.Databases
	if saved, openErr := openSavedConnections(cmd.ErrOrStderr()); openErr != nil {
		cmd.PrintErrf("Warning: saved connections are unavailable: %v\n", openErr)
	} else {
		prefsPath, databaseStore = saved.Path(), saved.Databases()
	}
	shared := sharedRunnerOptions(&cfg, registry, hosts.For(&cfg), mcpLogDir, kubeconfigDir, databaseStore)
	shared.In = cmd.InOrStdin()
	databases := shared.Databases

	// SSO sign-ins suspend the dashboard, so their browser prompt, URL and code stay readable.
	handoff := ui.NewTerminalHandoff()
	// Passwords the user asks to remember go to the OS keychain; only the wizard offers it.
	keychainStore := newKeychain()
	// The dashboard's runner follows config.json, the flags of this run applied over it: the next
	// openings and the wizard use its new values. It stays silent: its progress would corrupt the
	// full-screen display.
	tuiRunners := newRunnerSource(configPath, withOverrides(overridesFromFlags(cmd.Flags()), func(next config.Config) (*jumphost.Runner, error) {
		// Not "options": that is the parameter of executeJumphost.
		tuiOptions := sharedRunnerOptions(&next, registry, hosts.For(&next), mcpLogDir, kubeconfigDir, databaseStore)
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
	dashboard := ui.DashboardOptions{Handoff: handoff, Environments: cfg.EnvironmentSet()}
	if configPath != "" {
		dashboard.Settings = newConfigSettingsStore(configPath, cmd.Flags())
	}
	if databaseStore != nil {
		dashboard.Databases = databaseStore
	}
	if latestRelease != "" {
		dashboard.UpdateNotice = wizardUpdateNotice(latestRelease)
	}
	// The dashboard's sessions live in this process and end with it (CLI mode).
	manager := sessions.NewManager(cmd.Context(), sessions.Config{Runner: tuiRunners.Source()})
	dashboard.ProjectDir = projectDir

	// Without arguments, the dashboard opens right away on the wizard: one screen from start to end.
	if len(args) == 0 && strings.TrimSpace(options.ResourceType) == "" && interactive {
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
	if options.ResourceType == directResourceType {
		if options.Name, err = checkDirectTarget(cmd, args, databases); err != nil {
			return err
		}
		return openDirect(cmd, options, interactive, &directEnv{
			discovery: discovery, manager: manager, dashboard: &dashboard, shared: &shared, cfg: &cfg, prefsPath: prefsPath,
		})
	}
	if len(args) > 1 {
		options.Name = strings.TrimSpace(args[1])
	}
	if len(args) > 2 {
		localPort, convErr := strconv.Atoi(args[2])
		if convErr != nil {
			return fmt.Errorf("invalid local port %q: %w", args[2], convErr)
		}
		if localPort <= 0 {
			return fmt.Errorf("invalid local port %q: must be greater than 0", args[2])
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

// sharedRunnerOptions are the runner options of both modes, from the config and the directories
// airlock found; a nil databaseStore disables the direct databases.
func sharedRunnerOptions(cfg *config.Config, registry *jumphost.SessionRegistry, mcpHost *mcpserve.Host, mcpLogDir, kubeconfigDir string, databaseStore *connections.Databases) jumphost.RunnerOptions {
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
		Environments:      cfg.EnvironmentSet(),
	}
}

func promptResourceType(in io.Reader, out io.Writer) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("resource type is required (expected eks, rds or elasticache): airlock <type> [name] [local-port]")
	}

	_, _ = fmt.Fprint(out, "Please specify resource type (eks|rds|elasticache): ") //nolint:errcheck // best-effort terminal output
	var value string
	_, err := fmt.Fscanln(in, &value)
	if err != nil {
		return "", fmt.Errorf("failed to read resource type: %w", err)
	}

	value = strings.ToLower(strings.TrimSpace(value))
	if value != "eks" && value != "rds" && value != "elasticache" {
		return "", fmt.Errorf("invalid resource type %q (expected eks, rds or elasticache)", value)
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
