package cli

import (
	"os"

	"github.com/n2jsoft-public-org/airlock/internal/ui"
	"github.com/spf13/cobra"
)

// pickExports lets the user uncheck the choices to leave out of the export (or verb), on stderr so that the exported string
// alone reaches stdout, and returns the indexes of the checked ones; asked is false, and nothing
// is shown, when stdin or stderr is no terminal.
func pickExports(cmd *cobra.Command, title, verb string, choices []ui.ExportChoice) (indexes []int, asked bool, err error) {
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := cmd.ErrOrStderr().(*os.File)
	if len(choices) == 0 || !inOK || !outOK || !isInteractiveInput(in) || !isInteractiveInput(out) {
		return nil, false, nil
	}
	indexes, err = ui.PickExports(title, verb, choices, in, out)
	return indexes, true, err
}
