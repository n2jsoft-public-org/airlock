package cli

import (
	"errors"
	"strings"

	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

// dockerResourceType opens the database of a local Docker container: `airlock docker [container]`.
const dockerResourceType = "docker"

// dockerContainers is the discovery of the local Docker databases; nil, which hides them, without
// the docker CLI.
func dockerContainers() jumphost.DockerContainers {
	if !dockerdb.Available() {
		return nil
	}
	return dockerdb.Client{}
}

// checkDockerTarget refuses what does not apply to a Docker database and returns the container of
// `airlock docker [container]`; without one, only the dashboard can open (on the Docker list).
func checkDockerTarget(cmd *cobra.Command, args []string, containers jumphost.DockerContainers, interactive bool) (string, error) {
	for _, flag := range directRefusedFlags {
		if cmd.Flags().Changed(flag) {
			return "", i18n.NewError("cli.docker.does_not_apply_to", flag)
		}
	}
	if len(args) > 2 {
		return "", errors.New(i18n.T("cli.docker.local_port_does_not"))
	}
	if containers == nil {
		return "", errors.New(i18n.T("cli.docker.docker_cli_not_found"))
	}
	name := ""
	if len(args) > 1 {
		name = strings.TrimSpace(args[1])
	}
	if name == "" && !interactive {
		return "", errors.New(i18n.T("cli.docker.container_name_is_required"))
	}
	return name, nil
}

// dockerAllowWrites is the MCP write access of a Docker database: on unless --mcp-allow-writes
// says otherwise, a local container being a scratch database.
func dockerAllowWrites(cmd *cobra.Command, flagValue bool) bool {
	if cmd.Flags().Changed("mcp-allow-writes") {
		return flagValue
	}
	return true
}

// dockerOptions gives a Docker run its MCP write default. The dashboard keeps options as they are:
// they are the base of every later wizard, and the Docker review turns writes on by itself.
func dockerOptions(cmd *cobra.Command, options *jumphost.Options, interactive bool) *jumphost.Options {
	if interactive {
		return options
	}
	withWrites := *options
	withWrites.MCPAllowWrites = dockerAllowWrites(cmd, options.MCPAllowWrites)
	return &withWrites
}

// openDocker opens the Docker database of options: in a terminal, the dashboard on the Docker
// list, or on the review of the named container; otherwise a session serving its MCP server until
// the command is interrupted, signed in with the container's password.
func openDocker(cmd *cobra.Command, options *jumphost.Options, interactive bool, env *directEnv) error {
	options = dockerOptions(cmd, options, interactive)
	if interactive {
		dashboard := *env.dashboard
		dashboard.DockerAtStart, dashboard.OpenDocker = true, options.Name
		if shouldUseDaemon(env.cfg, interactive) {
			return runDaemonView(cmd, options, false, env.discovery, &dashboard)
		}
		return ui.RunJumphostSessions(cmd.Context(), env.manager, env.discovery, 0, *options, cmd.OutOrStdout(), dashboard)
	}
	runnerOptions := *env.shared
	runnerOptions.Out, runnerOptions.Err = cmd.OutOrStdout(), cmd.ErrOrStderr()
	return dockerRunError(jumphost.NewRunner(&runnerOptions).Run(cmd.Context(), options))
}

// dockerRunError explains a Docker session that cannot start without the dashboard: the runner's
// error already names where the container's password is set.
func dockerRunError(err error) error {
	switch {
	case errors.Is(err, jumphost.ErrPasswordRequired):
		return i18n.NewError("cli.docker.or_open_it_from", err)
	case errors.Is(err, jumphost.ErrSignInRejected):
		return i18n.NewError("cli.docker.set_right_password_on", err)
	default:
		return err
	}
}
