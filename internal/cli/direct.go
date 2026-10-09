package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

// directRefusedFlags are the connect flags with no meaning for a direct database: it is no AWS
// resource, and its user comes from its definition.
var directRefusedFlags = []string{"profile", "aws-profile", "region", "db-user", "db-secret"}

// checkDirectTarget refuses what does not apply to a direct database and returns the name of
// `airlock direct <name>`, which must be defined: nothing opens otherwise.
func checkDirectTarget(cmd *cobra.Command, args []string, databases jumphost.DirectDatabases) (string, error) {
	for _, flag := range directRefusedFlags {
		if cmd.Flags().Changed(flag) {
			return "", fmt.Errorf("--%s does not apply to direct databases", flag)
		}
	}
	if len(args) > 2 {
		return "", errors.New("a local port does not apply to direct databases")
	}
	name := ""
	if len(args) > 1 {
		name = strings.TrimSpace(args[1])
	}
	if name == "" {
		return "", errors.New("a direct database name is required: airlock direct <name> (see airlock connection list)")
	}
	if databases == nil {
		return "", errors.New("direct databases are unavailable: the config directory cannot be resolved")
	}
	_, found, err := databases.Get(name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", directdb.NotFoundError(name)
	}
	return name, nil
}

// directEnv is what executeJumphost prepared that a direct opening needs.
type directEnv struct {
	// discovery is the wizard discovery of the dashboard, which follows config.json.
	discovery ui.JumphostDiscovery
	manager   *sessions.Manager
	dashboard *ui.DashboardOptions
	shared    *jumphost.RunnerOptions
	cfg       *config.Config
	prefsPath string
}

// openDirect opens the direct database of options: in a terminal, the dashboard on its review
// step, where the password is typed or filled from the keychain; otherwise a session serving its
// MCP server until the command is interrupted.
func openDirect(cmd *cobra.Command, options *jumphost.Options, interactive bool, env *directEnv) error {
	if interactive {
		dashboard := *env.dashboard
		dashboard.OpenDirect = options.Name
		// In daemon mode the review step opens it in the daemon: the password is typed the same way.
		if shouldUseDaemon(env.cfg, interactive) {
			return runDaemonView(cmd, options, false, env.discovery, &dashboard)
		}
		return ui.RunJumphostSessions(cmd.Context(), env.manager, env.discovery, 0, *options, cmd.OutOrStdout(), dashboard)
	}
	runnerOptions := *env.shared
	runnerOptions.Out, runnerOptions.Err = cmd.OutOrStdout(), cmd.ErrOrStderr()
	// Without the dashboard, the password can only come from the keychain.
	runnerOptions.PrefsPath, runnerOptions.Keychain = env.prefsPath, newKeychain()
	return directRunError(options.Name, jumphost.NewRunner(&runnerOptions).Run(cmd.Context(), options))
}

// directRunError explains a direct session that cannot start without the dashboard.
func directRunError(name string, err error) error {
	switch {
	case errors.Is(err, jumphost.ErrPasswordRequired):
		return fmt.Errorf("no password remembered for %s: open it once from the dashboard with \"Remember in keychain\"", name)
	case errors.Is(err, jumphost.ErrSignInRejected):
		// The runner already forgot the rejected keychain password.
		return fmt.Errorf("%w: open it from the dashboard to enter the password again", err)
	default:
		return err
	}
}
