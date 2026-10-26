package cli

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newConfigOrganizationTokenCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "organization-token",
		Short: i18n.T("cli.config_organization_token.short"),
		Long:  i18n.T("cli.config_organization_token.long"),
		// The token is never an argument: it would stay in the shell history.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := newKeychain()
			if remove {
				if err := store.Delete(organizationTokenAccount); err != nil {
					return err
				}
				outf(cmd, i18n.T("cli.config_organization_token.deleted"))
				return nil
			}
			token, err := readOrganizationToken(cmd)
			if err != nil {
				return err
			}
			if token == "" {
				return i18n.NewError("cli.config_organization_token.empty")
			}
			if err := store.Set(organizationTokenAccount, token); err != nil {
				return err
			}
			outf(cmd, i18n.T("cli.config_organization_token.stored"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "delete", false, i18n.T("cli.config_organization_token.flag.delete"))
	return cmd
}

// readOrganizationToken reads the token without echoing it in a terminal, else one line of the
// standard input.
func readOrganizationToken(cmd *cobra.Command) (string, error) {
	if file, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		cmd.PrintErr(i18n.T("cli.config_organization_token.prompt"))
		token, err := term.ReadPassword(int(file.Fd()))
		cmd.PrintErrln()
		return strings.TrimSpace(string(token)), err
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
