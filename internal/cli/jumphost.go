package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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

	return cmd
}

func runJumphost(cmd *cobra.Command, args []string) error {
	return executeJumphost(cmd, args, jumphost.Options{})
}

func executeJumphost(cmd *cobra.Command, args []string, options jumphost.Options) error {
	if len(args) == 0 && strings.TrimSpace(options.ResourceType) == "" && isInteractiveInput(cmd.InOrStdin()) {
		discoveryRunner := jumphost.NewRunner(jumphost.RunnerOptions{
			In:  cmd.InOrStdin(),
			Out: io.Discard,
			Err: cmd.ErrOrStderr(),
		})

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
		In:              cmd.InOrStdin(),
		Out:             cmd.OutOrStdout(),
		Err:             cmd.ErrOrStderr(),
		SessionExecutor: ui.RunSSMSessionTUI,
	})

	return runner.Run(cmd.Context(), options)
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
