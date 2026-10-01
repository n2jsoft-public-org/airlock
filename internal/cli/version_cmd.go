package cli

import (
	"github.com/n2jsoft/airlock/internal/version"
	"github.com/spf13/cobra"
)

// newVersionCmd prints the commit airlock was built from, as YYYYMMDD-<short sha>.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the airlock version (commit date and short hash)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outf(cmd, "%s\n", version.String())
			return nil
		},
	}
}
