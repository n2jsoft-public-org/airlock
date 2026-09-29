package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/example/acme-cli/internal/appdir"
	"github.com/example/acme-cli/internal/dbmcp"
	"github.com/example/acme-cli/internal/jumphost"
	"github.com/example/acme-cli/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newJumphostCmd() *cobra.Command {
	options := jumphost.Options{}

	cmd := &cobra.Command{
		Use:   "jumphost [eks|rds|elasticache] [name] [local-port]",
		Short: "Connect to EKS, RDS, or ElastiCache through SSM jumphost",
		Long: "Simple utility to connect to a remote resource (EKS, RDS, ElastiCache) through SSM Session Manager. " +
			"When values are omitted in an interactive shell, the command prompts for them.",
		Args: cobra.MaximumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeJumphost(cmd, args, options)
		},
	}

	cmd.Flags().StringVarP(&options.AwsProfile, "profile", "p", "", "AWS profile to use (defaults to AWS_PROFILE or default)")
	cmd.Flags().StringVar(&options.AwsProfile, "aws-profile", "", "Alias of --profile")
	cmd.Flags().StringVar(&options.AwsRegion, "region", "", "AWS region to use (defaults to AWS_REGION, configured region, or eu-west-3)")
	cmd.Flags().BoolVar(&options.SkipIDEDataSource, "no-ide-datasource", false, "Do not register the connection in ./.idea/dataSources.xml")
	cmd.Flags().BoolVar(&options.SkipMCP, "no-mcp", false, "Do not serve an MCP server giving AI assistants access to the database")
	cmd.Flags().BoolVar(&options.MCPAllowWrites, "mcp-allow-writes", false, "Let the MCP server modify data, as the database user (master user secret on SQL Server)")
	cmd.Flags().StringVar(&options.DBUser, "db-user", "", "Database user for RDS AWS IAM authentication (defaults to "+jumphost.DefaultRDSIAMUser+")")

	return cmd
}

func runJumphost(cmd *cobra.Command, args []string) error {
	return executeJumphost(cmd, args, jumphost.Options{})
}

func executeJumphost(cmd *cobra.Command, args []string, options jumphost.Options) error {
	// Runners share the registry so parallel sessions never reuse a local port or target,
	// and the MCP host so all database sessions are served on one localhost listener.
	registry := jumphost.NewSessionRegistry()
	mcpHost := dbmcp.NewHost()
	interactive := isInteractiveInput(cmd.InOrStdin())
	// SQL run through MCP servers is logged to ~/.config/jumphost/logs; a lookup failure only disables the log.
	mcpLogDir, err := appdir.LogsDir()
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: MCP queries will not be logged: %v\n", err)
	}
	// The wizard's Connect step choices are remembered in ~/.config/jumphost/connections.json;
	// a lookup failure only disables it.
	prefsPath, err := appdir.ConnectionsFile()
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: connection choices will not be remembered: %v\n", err)
	}

	// The TUI runner stays silent: its progress would corrupt the full-screen display.
	tuiRunner := jumphost.NewRunner(jumphost.RunnerOptions{
		In:        cmd.InOrStdin(),
		Out:       io.Discard,
		Err:       io.Discard,
		Registry:  registry,
		MCPHost:   mcpHost,
		MCPLogDir: mcpLogDir,
		PrefsPath: prefsPath,
	})

	if len(args) == 0 && strings.TrimSpace(options.ResourceType) == "" && interactive {
		discoveryRunner := tuiRunner

		selectedOptions, canceled, err := ui.RunJumphostWizard(cmd.Context(), discoveryRunner, options)
		if err != nil {
			return err
		}
		if canceled {
			return nil
		}

		options = selectedOptions
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

	runner := jumphost.NewRunner(jumphost.RunnerOptions{
		In:        cmd.InOrStdin(),
		Out:       cmd.OutOrStdout(),
		Err:       cmd.ErrOrStderr(),
		Registry:  registry,
		MCPHost:   mcpHost,
		MCPLogDir: mcpLogDir,
	})

	if !interactive {
		return runner.Run(cmd.Context(), options)
	}

	session, err := runner.Prepare(cmd.Context(), options)
	if err != nil {
		return err
	}

	return ui.RunJumphostSessions(cmd.Context(), tuiRunner, session, options, cmd.OutOrStdout())
}

func promptResourceType(in io.Reader, out io.Writer) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("resource type is required (expected eks, rds or elasticache)")
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
