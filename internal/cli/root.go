package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/keychain"
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
		Use:   "airlock [eks|rds|elasticache] [name] [local-port]",
		Short: "Connect to EKS, RDS, or ElastiCache through an SSM jumphost",
		Long: "Connects to a private AWS resource (EKS, RDS, ElastiCache) through SSM Session Manager port forwarding. " +
			"When values are omitted in an interactive shell, a wizard asks for them.",
		Args:         cobra.MaximumNArgs(3),
		SilenceUsage: true,
		// main prints the returned error once.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeJumphost(cmd, args, options)
		},
	}

	cmd.Flags().StringVarP(&options.AwsProfile, "profile", "p", "", "AWS profile to use (the wizard asks when omitted)")
	cmd.Flags().StringVar(&options.AwsProfile, "aws-profile", "", "Alias of --profile")
	cmd.Flags().StringVar(&options.AwsRegion, "region", "", "AWS region to use (defaults to AWS_REGION, then the profile region)")
	cmd.Flags().BoolVar(&options.SkipIDEDataSource, "no-ide-datasource", false, "Do not register the connection in ./.idea/dataSources.xml")
	cmd.Flags().BoolVar(&options.SkipMCP, "no-mcp", false, "Do not serve an MCP server giving AI assistants access to the database")
	cmd.Flags().BoolVar(&options.MCPAllowWrites, "mcp-allow-writes", false, "Let the MCP server modify data, as the database user (master user secret on SQL Server)")
	cmd.Flags().StringVar(&options.DBUser, "db-user", "", "Database user for RDS AWS IAM authentication (defaults to \"defaultDbUser\" of the config file)")
	cmd.Flags().StringVar(&options.DBSecretARN, "db-secret", "", "Secrets Manager secret (ARN or name) holding the RDS password, as RDS JSON or plain text")

	cmd.Flags().String("jumphost-tag", "", `Tag of the SSM jumphost instance, as Key=Value (config "jumphostTag", default Usage=jumphost)`)
	cmd.Flags().String("read-only-secret-tag", "", `Tag of the read-only SQL Server secrets, as Key=Value (config "readOnlySecretTag")`)
	cmd.Flags().String("mcp-server-prefix", "", `Prefix of the MCP server names (config "mcpServerPrefix", default airlock)`)

	cmd.AddCommand(newConfigCmd())

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

func executeJumphost(cmd *cobra.Command, args []string, options jumphost.Options) error {
	if migrated, err := appdir.MigrateLegacy(); err != nil {
		cmd.PrintErrf("Warning: %v\n", err)
	} else if migrated {
		cmd.PrintErrln("Copied saved connections and MCP logs of the former jumphost config directory into airlock's.")
	}
	cfg, _, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	// Runners share the registry so parallel sessions never reuse a local port or target,
	// and the MCP host so all database sessions are served on one localhost listener.
	registry := jumphost.NewSessionRegistry()
	mcpHost := dbmcp.NewHost()
	interactive := isInteractiveInput(cmd.InOrStdin())
	// SQL run through MCP servers is logged to ~/.config/airlock/logs; a lookup failure only disables the log.
	mcpLogDir, err := appdir.LogsDir()
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: MCP queries will not be logged: %v\n", err)
	}
	// Each EKS session gets a kubeconfig in ~/.config/airlock/kube; a lookup failure only disables it.
	kubeconfigDir, err := appdir.KubeconfigDir()
	if err != nil {
		cmd.PrintErrf("Warning: EKS sessions will not get a kubeconfig: %v\n", err)
	}
	// The wizard's Connect step choices are remembered in ~/.config/airlock/connections.json;
	// a lookup failure only disables it.
	prefsPath, err := appdir.ConnectionsFile()
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: connection choices will not be remembered: %v\n", err)
	}

	shared := jumphost.RunnerOptions{
		In:                cmd.InOrStdin(),
		Registry:          registry,
		MCPHost:           mcpHost,
		MCPLogDir:         mcpLogDir,
		DefaultRegion:     cfg.DefaultRegion,
		DefaultDBUser:     cfg.DefaultDBUser,
		JumphostTag:       cfg.JumphostTag,
		ReadOnlySecretTag: cfg.ReadOnlySecretTag,
		MCPServerPrefix:   cfg.MCPServerPrefix,
		KubeconfigDir:     kubeconfigDir,
	}

	// The TUI runner stays silent: its progress would corrupt the full-screen display.
	tuiOptions := shared
	tuiOptions.Out, tuiOptions.Err, tuiOptions.PrefsPath = io.Discard, io.Discard, prefsPath
	// Passwords the user asks to remember go to the OS keychain; only the wizard offers it.
	tuiOptions.Keychain = keychain.NewOSStore()
	// SSO sign-ins suspend the dashboard, so their browser prompt, URL and code stay readable.
	handoff := ui.NewTerminalHandoff()
	tuiOptions.Interactive = handoff.Run
	tuiRunner := jumphost.NewRunner(tuiOptions)

	// Without arguments, the dashboard opens right away on the wizard: one screen from start to end.
	if len(args) == 0 && strings.TrimSpace(options.ResourceType) == "" && interactive {
		return ui.RunJumphostSessions(cmd.Context(), tuiRunner, nil, options, cmd.OutOrStdout(), handoff)
	}

	if len(args) > 0 {
		options.ResourceType = strings.ToLower(strings.TrimSpace(args[0]))
	}
	if len(args) > 1 {
		options.Name = strings.TrimSpace(args[1])
	}
	if len(args) > 2 {
		localPort, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("invalid local port %q: %w", args[2], err)
		}
		if localPort <= 0 {
			return fmt.Errorf("invalid local port %q: must be greater than 0", args[2])
		}
		options.LocalPortNumber = localPort
	}

	if options.ResourceType == "" {
		resourceType, err := promptResourceType(cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return err
		}
		options.ResourceType = resourceType
	}

	runnerOptions := shared
	runnerOptions.Out, runnerOptions.Err = cmd.OutOrStdout(), cmd.ErrOrStderr()
	runner := jumphost.NewRunner(runnerOptions)

	if !interactive {
		return runner.Run(cmd.Context(), options)
	}

	session, err := runner.Prepare(cmd.Context(), options)
	if err != nil {
		return err
	}

	return ui.RunJumphostSessions(cmd.Context(), tuiRunner, session, options, cmd.OutOrStdout(), handoff)
}

func promptResourceType(in io.Reader, out io.Writer) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("resource type is required (expected eks, rds or elasticache): airlock <type> [name] [local-port]")
	}

	_, _ = fmt.Fprint(out, "Please specify resource type (eks|rds|elasticache): ")
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
