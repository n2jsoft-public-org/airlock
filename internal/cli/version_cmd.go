package cli

import (
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/version"
	"github.com/spf13/cobra"
)

// newVersionCmd prints the release airlock was built from, such as v1.2.0.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: i18n.T("cli.version.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outf(cmd, "%s\n", version.String())
			return nil
		},
	}
}
