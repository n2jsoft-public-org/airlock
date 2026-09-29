package cli

import (
	"errors"
	"os"

	"github.com/example/acme-cli/internal/jumphost"
	"github.com/example/acme-cli/internal/ui"
	"github.com/spf13/cobra"
)

// Execute is the public entrypoint for the CLI.
func Execute() error {
	return NewRootCmd().Execute()
}

// NewRootCmd builds the root command and registers all subcommands.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          "acme",
		Short:        "acme company CLI helper",
		SilenceUsage: true,
		RunE:         runRootTUI,
	}

	rootCmd.AddCommand(
		newJumphostCmd(),
		newUpdateTranslateCmd(),
	)

	return rootCmd
}

func runRootTUI(cmd *cobra.Command, _ []string) error {
	selection, err := ui.RunCommandPicker()
	if err != nil {
		return err
	}

	if selection.AWSProfile != "" {
		_ = os.Setenv("AWS_PROFILE", selection.AWSProfile)
	}

	switch selection.Command {
	case "":
		return nil
	case "jumphost":
		return executeJumphost(cmd, nil, jumphost.Options{
			AwsProfile: selection.AWSProfile,
		})
	case "update-translate":
		return runUpdateTranslate(cmd, nil)
	default:
		return errors.New("unsupported command selection")
	}
}
