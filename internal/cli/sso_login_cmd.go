package cli

import (
	"github.com/n2jsoft/airlock/internal/awssso"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/spf13/cobra"
)

// newSSOLoginCmd signs a profile in; airlock runs it itself, on the user's terminal, when a
// profile's sign-in expired.
func newSSOLoginCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:    "sso-login",
		Short:  i18n.T("cli.sso_login.short"),
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return awssso.Login(cmd.Context(), profile, awssso.TerminalPrompt(cmd.ErrOrStderr()))
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", i18n.T("cli.sso_login.flag.profile"))
	_ = cmd.MarkFlagRequired("profile") //nolint:errcheck // the flag exists.
	return cmd
}
