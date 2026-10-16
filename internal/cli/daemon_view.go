package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/daemon"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// shouldUseDaemon reports whether this run is a view of the daemon.
func shouldUseDaemon(cfg *config.Config, interactive bool) bool {
	return cfg.Mode == config.ModeDaemon && interactive
}

// daemonSettingFlags are the setting flags a daemon view refuses: the daemon's runner reads
// config.json, never the flags of a view.
var daemonSettingFlags = []struct{ flag, key string }{
	{flag: "jumphost-tag", key: "aws.jumphostTag"},
	{flag: "read-only-secret-tag", key: "modules.sql.readOnlySecretTag"},
	{flag: "mcp-server-prefix", key: "mcp.serverPrefix"},
	{flag: "mcp-clients", key: "mcp.clients"},
}

// checkDaemonFlags refuses the setting flags that would only change the view, not the daemon.
func checkDaemonFlags(flags *pflag.FlagSet) error {
	for _, setting := range daemonSettingFlags {
		if flags.Changed(setting.flag) {
			return i18n.NewError("cli.daemon_view.does_not_apply_in",
				setting.flag, setting.key)
		}
	}
	return nil
}

// connectDaemon starts or reaches the daemon; another version is restarted when the user agrees.
func connectDaemon(cmd *cobra.Command) (*daemon.Client, error) {
	client, err := ensureDaemon(cmd.Context())
	var mismatch *daemon.VersionMismatchError
	if !errors.As(err, &mismatch) {
		return client, err
	}
	status, statusErr := client.Status(cmd.Context())
	if err := errors.Join(statusErr, client.Close()); err != nil {
		return nil, err
	}
	question := i18n.Format("cli.daemon_view.with_sessions_restart_it", mismatch, status.Sessions)
	if !confirm(cmd, question) {
		return nil, errors.New(i18n.T("cli.daemon_view.daemon_was_left_as"))
	}
	if _, err := stopDaemon(cmd.Context()); err != nil {
		return nil, err
	}
	return ensureDaemon(cmd.Context())
}

// connectDaemonQuiet starts or reaches the daemon without asking anything: a view that is running
// cannot prompt, so another version comes back as an error, shown in its banner.
func connectDaemonQuiet(ctx context.Context) (*daemon.Client, error) {
	client, err := ensureDaemon(ctx)
	if err != nil {
		if client != nil {
			_ = client.Close() //nolint:errcheck // reporting err.
		}
		return nil, err
	}
	return client, nil
}

// terminalInteractive runs a command on the terminal before the dashboard starts.
func terminalInteractive(cmd *cobra.Command) jumphost.InteractiveFunc {
	return func(ctx context.Context, purpose, name string, args ...string) error {
		cmd.PrintErrln(purpose)
		run := exec.CommandContext(ctx, name, args...) // #nosec G204 -- aws sso login, built by the runner.
		run.Stdin, run.Stdout, run.Stderr = os.Stdin, os.Stdout, os.Stderr
		return run.Run()
	}
}

// runDaemonView shows the daemon's sessions; when openFirst, the target of options is opened first.
func runDaemonView(cmd *cobra.Command, options *jumphost.Options, openFirst bool, discovery ui.JumphostDiscovery, dashboard *ui.DashboardOptions) error {
	if err := checkDaemonFlags(cmd.Flags()); err != nil {
		return err
	}
	client, err := connectDaemon(cmd)
	if err != nil {
		return err
	}
	// The daemon and its sessions keep running without this view; the client in use is the last
	// one reconnect returned.
	var (
		lock     sync.Mutex
		current  = client
		finished bool
	)
	defer func() {
		lock.Lock()
		defer lock.Unlock()
		finished = true
		_ = current.Close() //nolint:errcheck // nothing to do about it.
	}()
	view := *dashboard
	view.Detach = true
	if status, statusErr := client.Status(cmd.Context()); statusErr == nil {
		view.Notice = gatewayNotice(&status)
	}
	view.Reconnect = func(ctx context.Context) (sessions.Service, error) {
		next, connectErr := connectDaemonQuiet(ctx)
		if connectErr != nil {
			return nil, connectErr
		}
		lock.Lock()
		defer lock.Unlock()
		if finished {
			_ = next.Close() //nolint:errcheck // the view ended meanwhile.
			return nil, errors.New(i18n.T("cli.daemon_view.view_ended"))
		}
		_ = current.Close() //nolint:errcheck // the connection was lost already.
		current = next
		return next, nil
	}
	base := *options
	var first sessions.ID
	if openFirst {
		base.ProjectDir = view.ProjectDir
		if first, err = openInDaemon(cmd, client, &base, terminalInteractive(cmd)); err != nil {
			return err
		}
	}
	return ui.RunJumphostSessions(cmd.Context(), client, discovery, first, base, cmd.OutOrStdout(), view)
}

// openInDaemon returns the session of options in the daemon: the running session of that target
// when there is one (see runningSessionOf), otherwise a new opening.
func openInDaemon(cmd *cobra.Command, service sessions.Service, options *jumphost.Options, interactive jumphost.InteractiveFunc) (sessions.ID, error) {
	snapshot, err := service.Snapshot(cmd.Context())
	if err != nil {
		return 0, err
	}
	if id := runningSessionOf(snapshot.Sessions, options); id != 0 {
		return id, nil
	}
	cmd.PrintErr(i18n.Format("cli.daemon_view.opening_in_airlock_daemon", options.ResourceType, options.Name))
	return service.Open(cmd.Context(), sessions.OpenRequest{Options: *options, Interactive: interactive})
}

// runningSessionOf returns the running session of the target of options, 0 when none. It is that
// target when its type and name match, and its profile: the one options gives, else AWS_PROFILE,
// else default, as the runner resolves it. The region only counts when options gives it: the
// daemon resolves a missing one from the profile's configuration. Names match in any case.
func runningSessionOf(list []sessions.Session, options *jumphost.Options) sessions.ID {
	profile := strings.TrimSpace(options.AwsProfile)
	if profile == "" {
		profile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	if profile == "" {
		profile = "default"
	}
	region := strings.TrimSpace(options.AwsRegion)
	for index := range list {
		s := &list[index]
		if s.Active() && strings.EqualFold(s.ResourceType, strings.TrimSpace(options.ResourceType)) &&
			strings.EqualFold(s.Target, strings.TrimSpace(options.Name)) && strings.EqualFold(s.Profile, profile) &&
			(region == "" || strings.EqualFold(s.Region, region)) {
			return s.ID
		}
	}
	return 0
}
