package cli

import (
	"strings"

	"github.com/example/acme-cli/internal/ui"
	updatetranslate "github.com/example/acme-cli/internal/update_translate"
	"github.com/spf13/cobra"
)

func newUpdateTranslateCmd() *cobra.Command {
	commandOptions := updateTranslateCommandOptions{overwrite: true}

	cmd := &cobra.Command{
		Use:   "update-translate",
		Short: "Download and extract latest translation bundle",
		Long: "Downloads i18n_acme.zip from the latest release of example/translate via gh CLI " +
			"and extracts it into a destination folder.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeUpdateTranslate(cmd, commandOptions)
		},
	}

	cmd.Flags().StringVarP(&commandOptions.destination, "dest", "d", "", "Destination folder (defaults to current working directory)")
	cmd.Flags().BoolVar(&commandOptions.overwrite, "overwrite", true, "Overwrite existing files in destination")
	cmd.Flags().BoolVar(&commandOptions.noTUI, "no-tui", false, "Run without interactive wizard")

	return cmd
}

func runUpdateTranslate(cmd *cobra.Command, _ []string) error {
	return executeUpdateTranslate(cmd, updateTranslateCommandOptions{overwrite: true})
}

type updateTranslateCommandOptions struct {
	destination string
	overwrite   bool
	noTUI       bool
}

func executeUpdateTranslate(cmd *cobra.Command, commandOptions updateTranslateCommandOptions) error {
	runnerOptions := updatetranslate.Options{
		Destination:       commandOptions.destination,
		OverwriteExisting: commandOptions.overwrite,
	}

	if !commandOptions.noTUI && isInteractiveInput(cmd.InOrStdin()) && strings.TrimSpace(commandOptions.destination) == "" {
		selectedOptions, canceled, err := ui.RunUpdateTranslateWizard(runnerOptions)
		if err != nil {
			return err
		}
		if canceled {
			return nil
		}

		runnerOptions = selectedOptions
	}

	runner := updatetranslate.NewRunner(updatetranslate.RunnerOptions{
		Out: cmd.OutOrStdout(),
		Err: cmd.ErrOrStderr(),
	})

	return runner.Run(cmd.Context(), runnerOptions)
}
