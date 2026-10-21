package cli

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/orgconfig"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

// firstRunNeeded reports whether the dashboard launch opens the first-run wizard: config.json at
// path does not exist yet. A skipped wizard writes it, so it asks once.
func firstRunNeeded(path string, dashboard bool) bool {
	if !dashboard || path == "" {
		return false
	}
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// newSetupCmd opens the first-run wizard again, over the current config.json.
func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: i18n.T("cli.setup.short"),
		Long:  i18n.T("cli.setup.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isInteractiveInput(cmd.InOrStdin()) {
				return i18n.NewError("cli.setup.needs_terminal")
			}
			path, err := appdir.ConfigFile()
			if err != nil {
				return err
			}
			return runFirstRun(cmd, path, false)
		},
	}
}

// runFirstRun asks the first-run settings and writes them to path, then runs airlock doctor when
// asked; beforeDashboard waits for enter after it, the dashboard then taking the screen. A quit
// (ctrl+c) exits airlock.
func runFirstRun(cmd *cobra.Command, path string, beforeDashboard bool) error {
	ctx := cmd.Context()
	env := orgEnvironment()
	state := resolveOrganization(ctx, env, path)
	local, err := config.Load(path, nil)
	if err != nil {
		return err
	}
	// org is the organization over source, read again only when the user gives another one.
	org, source := state.Org, local.OrganizationConfig
	store := func() *configSettingsStore {
		s := newConfigSettingsStore(path, org, cmd.Flags())
		s.managed = state.Managed
		return s
	}
	snapshot, err := store().Snapshot()
	if err != nil {
		return err
	}
	setup := ui.FirstRunSetup{
		Snapshot: snapshot,
		Load: func(raw string) (ui.SettingsSnapshot, error) {
			if !state.Managed && raw != source {
				read, readErr := readOrganization(ctx, env, raw)
				if readErr != nil {
					return ui.SettingsSnapshot{}, readErr
				}
				org, source = read, raw
			}
			return store().Snapshot()
		},
		Validate: store().Validate,
		Save: func(values [][2]string) error {
			// Creates the file, even without any value: the wizard then does not open again.
			if saveErr := config.DropKeys(path, nil); saveErr != nil {
				return saveErr
			}
			s := store()
			for _, value := range values {
				if saveErr := s.Set(value[0], value[1]); saveErr != nil {
					return saveErr
				}
			}
			return nil
		},
		Profiles: awsapi.Profiles,
		SetLanguage: func(value string) {
			if value == config.LanguageAuto {
				value = i18n.Detect(os.Getenv, i18n.OSLocale)
			}
			i18n.SetLanguage(value)
		},
	}
	if state.Managed {
		setup.Managed = state.Source
	}
	result, err := ui.RunFirstRun(setup, cmd.InOrStdin(), cmd.OutOrStdout())
	switch {
	case err != nil:
		return err
	case !result.Saved:
		return &ExitCodeError{Code: 130}
	case !result.Doctor:
		if !result.Skipped {
			cmd.PrintErr(i18n.T("cli.first_run.doctor_hint"))
		}
		return nil
	}
	if _, err = ui.ShowDoctor(doctorSections(ctx, "", ""), cmd.InOrStdin(), cmd.OutOrStdout()); err != nil && !errors.Is(err, ui.ErrDoctorCanceled) {
		return err
	}
	if beforeDashboard {
		cmd.PrintErr(i18n.T("cli.first_run.press_enter"))
		// Any answer, or none, opens the dashboard.
		_, _ = bufio.NewReader(cmd.InOrStdin()).ReadString('\n') //nolint:errcheck // see above.
	}
	return nil
}

// readOrganization reads and validates the organization config at raw, nil for "".
func readOrganization(ctx context.Context, env orgEnv, raw string) (*config.Organization, error) {
	if raw == "" {
		return nil, nil
	}
	src, err := orgconfig.ParseSource(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, firstReadTimeout)
	defer cancel()
	got, err := env.fetcher.Fetch(ctx, src, "")
	if err != nil {
		return nil, errors.New(organizationWarning(src.Display(), err))
	}
	org, _, err := config.ParseOrganization(src.Display(), got.Body)
	return org, err
}
