package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/n2jsoft/airlock/internal/jumphost"
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
			return "", fmt.Errorf("failed to read the password: %w", err)
		}
		return string(password), nil
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("failed to read the password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// directResourceType is the resource type of the direct databases in prefs and keychain keys.
const directResourceType = "direct"

// checkDirectSignIn signs in to a direct database and runs SELECT 1; tests replace it, having no database.
var checkDirectSignIn = func(ctx context.Context, runner *jumphost.Runner, name, password string) error {
	return runner.CheckDirectSignIn(ctx, name, password)
}

func newDBTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Sign in to a direct database and run SELECT 1",
		Long: "Signs in with the password remembered in the OS keychain, else asks it; the typed password is " +
			"neither shown nor saved.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDirectDatabase,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openDatabaseStore()
			if err != nil {
				return err
			}
			db, err := getDatabase(store, args[0])
			if err != nil {
				return err
			}
			runner, err := newDBRunner(cmd, store)
			if err != nil {
				return err
			}
			password, remembered := runner.LoadKeychainPassword(cmd.Context(),
				jumphost.Options{ResourceType: directResourceType, Name: db.Name, DBUser: db.User})
			if !remembered {
				if password, err = promptPassword(cmd, fmt.Sprintf("Password of %s on %s: ", db.User, db.Name)); err != nil {
					return err
				}
				if password == "" {
					return errors.New("no password given")
				}
			}
			if warning := jumphost.DirectReadOnlyWarning(&db); warning != "" {
				outf(cmd, "Warning: %s\n", warning)
			}
			if err := checkDirectSignIn(cmd.Context(), runner, db.Name, password); err != nil {
				return err
			}
			outf(cmd, "OK: signed in to %s (%s) as %s\n", db.Name, db.Address(), db.User)
			return nil
		},
	}
}
