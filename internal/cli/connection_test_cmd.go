package cli

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// promptPassword asks a password on standard error: without echo on a terminal, else as one line
// of the input (pipes and tests).
var promptPassword = func(cmd *cobra.Command, prompt string) (string, error) {
	cmd.PrintErr(prompt)
	if file, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		password, err := term.ReadPassword(int(file.Fd()))
		cmd.PrintErrln()
		if err != nil {
			return "", i18n.NewError("cli.connection_test.failed_to_read_password", err)
		}
		return string(password), nil
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", i18n.NewError("cli.connection_test.failed_to_read_password", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// directResourceType is the resource type of the direct databases in prefs and keychain keys.
const directResourceType = "direct"

// checkDirectSignIn signs in to a direct database and runs SELECT 1; tests replace it, having no database.
var checkDirectSignIn = func(ctx context.Context, runner *jumphost.Runner, name, password string) error {
	return runner.CheckDirectSignIn(ctx, name, password)
}

func newConnectionTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "test <name>",
		Short:             i18n.T("cli.connection_test.short"),
		Long:              i18n.T("cli.connection_test.long"),
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDirectDatabase,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabases(cmd)
			if err != nil {
				return err
			}
			db, err := getDatabase(store, args[0])
			if err != nil {
				return err
			}
			runner := newConnectionRunner(cmd, store)
			password, remembered := runner.LoadKeychainPassword(cmd.Context(),
				jumphost.Options{ResourceType: directResourceType, Name: db.Name, DBUser: db.User})
			if !remembered {
				if password, err = promptPassword(cmd, i18n.Format("cli.connection_test.password_of_on", db.User, db.Name)); err != nil {
					return err
				}
				if password == "" {
					return errors.New(i18n.T("cli.connection_test.no_password_given"))
				}
			}
			if warning := jumphost.DirectReadOnlyWarning(&db); warning != "" {
				outf(cmd, i18n.T("cli.warning_s"), warning)
			}
			if err := checkDirectSignIn(cmd.Context(), runner, db.Name, password); err != nil {
				return err
			}
			outf(cmd, i18n.T("cli.connection_test.ok_signed_in_to"), db.Name, db.Address(), db.User)
			return nil
		},
	}
}
