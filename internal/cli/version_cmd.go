package cli

import (
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/version"
	"github.com/spf13/cobra"
)

// newVersionCmd prints the commit airlock was built from, as YYYYMMDD-<short sha>.
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
