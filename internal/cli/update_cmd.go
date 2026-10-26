package cli

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/selfupdate"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/n2jsoft/airlock/internal/version"
	"github.com/spf13/cobra"
)

// noTrackingEnv, set to any value, stops airlock update from adding InstallReaction to the release
// it installs.
const noTrackingEnv = "AIRLOCK_NO_TRACKING"

// reactionTimeout bounds the reaction to the installed release, which never holds an update back.
const reactionTimeout = 5 * time.Second

// updateEnv is what airlock update and the launch notice take from outside; tests replace it.
type updateEnv struct {
	token      func(context.Context) (string, error)
	repository func() (string, error)
	executable func() (string, error)
	cacheFile  func() (string, error)
	markerFile func() (string, error)
	// daemonRunning reports whether a daemon runs, which keeps the replaced binary until restarted; nil means none.
	daemonRunning func(context.Context) bool
	current       string
	baseURL       string
}

func defaultUpdateEnv() *updateEnv {
	return &updateEnv{
		token:         selfupdate.Token,
		repository:    selfupdate.Repository,
		executable:    selfupdate.Executable,
		cacheFile:     appdir.UpdateCheckFile,
		markerFile:    appdir.InstallMarkerFile,
		daemonRunning: func(ctx context.Context) bool { return findDaemon(ctx) != nil },
		current:       version.String(),
		baseURL:       selfupdate.DefaultBaseURL,
	}
}

// updater resolves the repository and the token; without a token it reads GitHub anonymously.
func (e *updateEnv) updater(ctx context.Context) (selfupdate.Updater, error) {
	repository, err := e.repository()
	if err != nil {
		return selfupdate.Updater{}, err
	}
	token, err := e.token(ctx)
	if err != nil && !errors.Is(err, selfupdate.ErrNoToken) {
		return selfupdate.Updater{}, err
	}
	return selfupdate.Updater{
		Client:  selfupdate.NewClient(e.baseURL, repository, token),
		Current: e.current,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}, nil
}

// newUpdateCmd installs the latest release of airlock in place of the running binary.
func newUpdateCmd() *cobra.Command {
	return newUpdateCmdWith(defaultUpdateEnv())
}

func newUpdateCmdWith(env *updateEnv) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: i18n.T("cli.update.short"),
		Long:  i18n.T("cli.update.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd, env, check, force)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, i18n.T("cli.update.flag.check"))
	cmd.Flags().BoolVar(&force, "force", false, i18n.T("cli.update.flag.force"))
	return cmd
}

func runUpdate(cmd *cobra.Command, env *updateEnv, check, force bool) error {
	ctx := cmd.Context()
	target, err := env.executable()
	if err != nil {
		return err
	}
	if err = selfupdate.CleanupOld(target); err != nil {
		cmd.PrintErr(i18n.Format("cli.warning", err))
	}
	updater, err := env.updater(ctx)
	if err != nil {
		return err
	}
	out := ui.NewUpdateOutput(cmd.InOrStdin(), cmd.OutOrStdout())
	var result selfupdate.Result
	var changes []ui.ReleaseNotes
	install := !check && force
	err = out.Step(i18n.T("cli.update.step.check"), func() (string, error) {
		if result, err = updater.Check(ctx); err != nil {
			return "", err
		}
		// Without a release yet there is nothing to install, even with --force.
		install = result.Latest != "" && (install || (!check && result.Status == selfupdate.StatusAvailable))
		changes = releaseChanges(ctx, updater.Client, &result)
		if install {
			return i18n.Format("cli.update.latest_release", result.Latest, result.Current), nil
		}
		return updateStatusMessage(&result), nil
	})
	if err != nil {
		return err
	}
	if !install {
		out.Changes(i18n.T("cli.update.whats_new"), changes)
		return nil
	}
	err = out.Step(i18n.Format("cli.update.step.install", result.Latest), func() (string, error) {
		return i18n.Format("cli.update.installed", result.Latest, target), updater.Install(ctx, &result, target)
	})
	if err != nil {
		return err
	}
	// The notice stops at once: the cache now describes the installed build.
	if path, pathErr := env.cacheFile(); pathErr == nil {
		installed := selfupdate.Result{Current: result.Latest, Latest: result.Latest, Status: selfupdate.StatusUpToDate}
		if saveErr := selfupdate.SaveCache(path, installed.CacheAt(time.Now())); saveErr != nil {
			cmd.PrintErr(i18n.Format("cli.warning", saveErr))
		}
	}
	if path, pathErr := env.markerFile(); pathErr == nil {
		marker := selfupdate.InstallMarker{Method: selfupdate.MethodScript, Path: target}
		if writeErr := selfupdate.WriteMarker(path, marker); writeErr != nil {
			cmd.PrintErr(i18n.Format("cli.warning", writeErr))
		}
	}
	reactToInstalledRelease(ctx, updater.Client, result.Release)
	out.Changes(i18n.T("cli.update.whats_new"), changes)
	out.Done(i18n.Format("cli.update.updated_airlock", result.Current, result.Latest))
	if env.daemonRunning != nil && env.daemonRunning(ctx) {
		out.Note(i18n.T("cli.update.restart_daemon_to_use"))
	}
	return nil
}

// releaseChanges is the notes of the releases an update from r.Current to r.Latest brings, newest
// first. It is best effort: without them the update goes on.
func releaseChanges(ctx context.Context, client *selfupdate.Client, r *selfupdate.Result) []ui.ReleaseNotes {
	if r.Status != selfupdate.StatusAvailable {
		return nil
	}
	releases, err := client.Releases(ctx)
	if err != nil {
		return nil
	}
	var notes []ui.ReleaseNotes
	for _, release := range selfupdate.Changes(releases, r.Current, r.Latest) {
		notes = append(notes, ui.ReleaseNotes{Tag: release.Tag, Body: release.Body})
	}
	return notes
}

// reactToInstalledRelease adds InstallReaction to release, so its reactions tell who installed it.
// It is best effort: a token without write access or an unreachable GitHub changes nothing, and
// without a token there is no account to react as.
func reactToInstalledRelease(ctx context.Context, client *selfupdate.Client, release selfupdate.Release) {
	if os.Getenv(noTrackingEnv) != "" || client.Token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, reactionTimeout)
	defer cancel()
	_ = client.React(ctx, release, selfupdate.InstallReaction) //nolint:errcheck // best effort: the update is done either way.
}

func updateStatusMessage(r *selfupdate.Result) string {
	switch r.Status {
	case selfupdate.StatusUpToDate:
		return i18n.Format("cli.update.airlock_is_up_to", r.Current)
	case selfupdate.StatusAvailable:
		return i18n.Format("cli.update.airlock_is_available_this", r.Latest, r.Current)
	case selfupdate.StatusNewer:
		return i18n.Format("cli.update.this_build_is_newer", r.Current, r.Latest, r.Latest)
	default:
		return i18n.Format("cli.update.this_airlock_was_built", r.Latest)
	}
}
