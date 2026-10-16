package cli

import (
	"errors"
	"strings"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/orgconfig"
	"github.com/spf13/cobra"
)

func newConfigSyncCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: i18n.T("cli.config_sync.short"),
		Long:  i18n.T("cli.config_sync.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			org, err := syncOrganization(cmd, path)
			if err != nil || org == nil {
				return err
			}
			return syncShadowedKeys(cmd, path, org, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("cli.config_sync.flag.yes"))
	return cmd
}

// syncOrganization reads the organization source of path now, a remote one into the cache, and
// prints its changes once; nil when no organization config applies.
func syncOrganization(cmd *cobra.Command, path string) (*config.Organization, error) {
	env := orgEnvironment()
	state := resolveOrganization(cmd.Context(), env, path)
	// Without an organization, the resolve already failed to read it: no second wait on it.
	if state.Org != nil && state.src.Raw != "" && state.src.Kind != orgconfig.KindFile {
		if err := refreshOrganization(cmd.Context(), env, state.src); err != nil {
			// Refresh leaves the cache as it was.
			return nil, errors.New(organizationWarning(state.Source, err))
		}
		state = resolveOrganization(cmd.Context(), env, path)
	}
	// An invalid or unreadable source, or an invalid managed file, is a failure of the sync.
	if state.Org == nil && len(state.Warnings) > 0 {
		return nil, errors.New(strings.Join(state.Warnings, "\n"))
	}
	for _, warning := range state.Warnings {
		cmd.PrintErr(i18n.Format("cli.warning", warning))
	}
	if state.Source == "" {
		// The resolver ignores an invalid config.json, organizationConfig included: report it.
		if _, err := config.Load(path, nil); err != nil {
			return nil, err
		}
		outf(cmd, i18n.T("cli.config_sync.no_organization"))
		return nil, nil
	}
	diff := organizationDiff(&state)
	if diff == nil {
		outf(cmd, i18n.T("cli.config_sync.up_to_date"), state.Source)
		return state.Org, nil
	}
	for _, line := range diff {
		outf(cmd, "%s\n", line)
	}
	if err := clearOrganizationDiff(env, state.src); err != nil {
		cmd.PrintErr(i18n.Format("cli.warning", err))
	}
	return state.Org, nil
}

// syncShadowedKeys removes from path, once confirmed, the keys repeating the value of org, and
// lists those overriding it.
func syncShadowedKeys(cmd *cobra.Command, path string, org *config.Organization, yes bool) error {
	equal, differing, err := config.ShadowedKeys(path, org)
	if err != nil {
		return err
	}
	for _, key := range differing {
		outf(cmd, i18n.T("cli.config_sync.differing"), key)
	}
	if len(equal) == 0 {
		return nil
	}
	keys := strings.Join(equal, ", ")
	outf(cmd, i18n.T("cli.config_sync.equal"), keys)
	switch {
	case yes:
	case !isInteractiveInput(cmd.InOrStdin()):
		outf(cmd, i18n.T("cli.config_sync.run_with_yes"))
		return nil
	case !confirm(cmd, i18n.Format("cli.config_sync.remove_confirm", path)):
		outf(cmd, i18n.T("cli.config_sync.kept"))
		return nil
	}
	if err := config.DropKeys(path, equal); err != nil {
		return err
	}
	outf(cmd, i18n.T("cli.config_sync.removed"), keys, path)
	return nil
}
