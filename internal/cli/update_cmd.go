package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/selfupdate"
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
	// daemonRunning reports whether a daemon runs, which keeps the replaced binary until restarted; nil means none.
	daemonRunning func(context.Context) bool
	current       string
	baseURL       string
}

func defaultUpdateEnv() updateEnv {
	return updateEnv{
		token:         selfupdate.Token,
		repository:    selfupdate.Repository,
		executable:    selfupdate.Executable,
		cacheFile:     appdir.UpdateCheckFile,
		daemonRunning: func(ctx context.Context) bool { return findDaemon(ctx) != nil },
		current:       version.String(),
		baseURL:       selfupdate.DefaultBaseURL,
	}
}

// updater resolves the repository and the token; it fails without GitHub access.
func (e updateEnv) updater(ctx context.Context) (selfupdate.Updater, error) {
	repository, err := e.repository()
	if err != nil {
		return selfupdate.Updater{}, err
	}
	token, err := e.token(ctx)
	if err != nil {
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

func newUpdateCmdWith(env updateEnv) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update airlock to the latest release",
		Long: "Checks the latest GitHub release of airlock and, when it is newer than this build, installs it " +
			"in place of the running binary. GitHub access comes from GH_TOKEN, GITHUB_TOKEN or the GitHub CLI (gh auth login).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd, env, check, force)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "Install the latest release even when this build is newer, diverged or unversioned")
	return cmd
}

func runUpdate(cmd *cobra.Command, env updateEnv, check, force bool) error {
	ctx := cmd.Context()
	target, err := env.executable()
	if err != nil {
		return err
	}
	if err = selfupdate.CleanupOld(target); err != nil {
		cmd.PrintErrf("Warning: %v\n", err)
	}
	updater, err := env.updater(ctx)
	if err != nil {
		return err
	}
	result, err := updater.Check(ctx)
	if err != nil {
		return err
	}
	if check || (result.Status != selfupdate.StatusAvailable && !force) {
		outf(cmd, "%s\n", updateStatusMessage(&result))
		return nil
	}
	if err := updater.Install(ctx, &result, target); err != nil {
		return err
	}
	// The notice stops at once: the cache now describes the installed build.
	if path, pathErr := env.cacheFile(); pathErr == nil {
		installed := selfupdate.Result{Current: result.Latest, Latest: result.Latest, Status: selfupdate.StatusUpToDate}
		if saveErr := selfupdate.SaveCache(path, installed.CacheAt(time.Now())); saveErr != nil {
			cmd.PrintErrf("Warning: %v\n", saveErr)
		}
	}
	reactToInstalledRelease(ctx, updater.Client, result.Release)
	outf(cmd, "Updated airlock %s → %s\n", result.Current, result.Latest)
	if env.daemonRunning != nil && env.daemonRunning(ctx) {
		outf(cmd, "restart the daemon to use the new version: airlock daemon restart\n")
	}
	return nil
}

// reactToInstalledRelease adds InstallReaction to release, so its reactions tell who installed it.
// It is best effort: a token without write access or an unreachable GitHub changes nothing.
func reactToInstalledRelease(ctx context.Context, client *selfupdate.Client, release selfupdate.Release) {
	if os.Getenv(noTrackingEnv) != "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, reactionTimeout)
	defer cancel()
	_ = client.React(ctx, release, selfupdate.InstallReaction) //nolint:errcheck // best effort: the update is done either way.
}

func updateStatusMessage(r *selfupdate.Result) string {
	switch r.Status {
	case selfupdate.StatusUpToDate:
		return fmt.Sprintf("airlock %s is up to date", r.Current)
	case selfupdate.StatusAvailable:
		return fmt.Sprintf("airlock %s is available (this build is %s): run airlock update", r.Latest, r.Current)
	case selfupdate.StatusNewer:
		return fmt.Sprintf("this build (%s) is newer than the latest release (%s): nothing to do (--force installs %s)", r.Current, r.Latest, r.Latest)
	case selfupdate.StatusDiverged:
		return fmt.Sprintf("this build (%s) comes from a commit not on main: nothing to do (--force installs %s)", r.Current, r.Latest)
	default:
		return fmt.Sprintf("this airlock was built without a version (go run): install it with make install (--force installs %s)", r.Latest)
	}
}
