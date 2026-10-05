package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/daemon"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/platform"
	"github.com/n2jsoft/airlock/internal/version"
	"github.com/spf13/cobra"
)

// daemonStopTimeout bounds `airlock daemon stop`.
const daemonStopTimeout = 45 * time.Second

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background daemon of the daemon mode",
		Long:  `With "mode": "daemon", a background daemon keeps the sessions; each airlock is a view of it.`,
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newDaemonStatusCmd(), newDaemonStopCmd(), newDaemonRestartCmd(), newDaemonRunCmd())
	return cmd
}

func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Tell whether the daemon runs, with its sessions and views", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := daemon.DefaultPaths()
			if err != nil {
				return err
			}
			client, err := daemon.Running(cmd.Context(), paths, version.String())
			if client == nil {
				outf(cmd, "The airlock daemon is not running (socket %s).\n", paths.Socket)
				return nil
			}
			defer func() { _ = client.Close() }() //nolint:errcheck // a status read only.
			status, statusErr := client.Status(cmd.Context())
			if statusErr != nil {
				return statusErr
			}
			outf(cmd, "The airlock daemon is running.\n  pid       %d\n  version   %s\n  since     %s\n  sessions  %d\n  views     %d\n  socket    %s\n  log       %s\n",
				status.PID, status.Version, status.StartedAt.Format(time.RFC1123), status.Sessions, status.Views, paths.Socket, daemonLogPath())
			if mismatch, ok := errors.AsType[*daemon.VersionMismatchError](err); ok {
				outf(cmd, "It runs another version than this airlock (%s): `airlock daemon restart` updates it.\n", mismatch.Client)
			}
			return nil
		},
	}
}

func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use: "stop", Short: "Stop every session, then the daemon", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stopped, err := stopDaemon(cmd.Context())
			if err != nil {
				return err
			}
			if stopped {
				outf(cmd, "The airlock daemon stopped.\n")
			} else {
				outf(cmd, "The airlock daemon is not running.\n")
			}
			return nil
		},
	}
}

func newDaemonRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use: "restart", Short: "Stop the daemon and its sessions, then start it again", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := stopDaemon(cmd.Context()); err != nil {
				return err
			}
			client, err := ensureDaemon(cmd.Context())
			if err != nil {
				return err
			}
			_ = client.Close() //nolint:errcheck // the daemon keeps running without this client.
			outf(cmd, "The airlock daemon restarted.\n")
			return nil
		},
	}
}

func newDaemonRunCmd() *cobra.Command {
	return &cobra.Command{
		Use: "run", Short: "Run the daemon in the foreground (started by airlock in daemon mode)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runDaemon(cmd) },
	}
}

// findDaemon describes the running daemon; nil when none answers or its files cannot be located.
func findDaemon(ctx context.Context) *runningDaemon {
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return nil
	}
	client, _ := daemon.Running(ctx, paths, version.String()) //nolint:errcheck // a version mismatch comes with a usable client.
	if client == nil {
		return nil
	}
	defer func() { _ = client.Close() }() //nolint:errcheck // a status read only.
	found := &runningDaemon{PID: client.Hello().PID}
	if status, err := client.Status(ctx); err == nil {
		found.Sessions = status.Sessions
	}
	return found
}

// stopDaemon stops a running daemon and waits for its lock to be free; false when none ran.
func stopDaemon(ctx context.Context) (bool, error) {
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return false, err
	}
	// A daemon of another version still stops: its client is usable for that.
	client, _ := daemon.Running(ctx, paths, version.String()) //nolint:errcheck // a version mismatch comes with a usable client.
	if client == nil {
		return false, nil
	}
	defer func() { _ = client.Close() }() //nolint:errcheck // the daemon is gone: nothing to do about it.
	ctx, cancel := context.WithTimeout(ctx, daemonStopTimeout)
	defer cancel()
	if err := client.StopDaemon(ctx); err != nil {
		return false, err
	}
	select {
	case <-client.Done():
	case <-ctx.Done():
		return false, errors.New("the airlock daemon did not stop in time")
	}
	if err := waitDaemonUnlocked(ctx, paths.Lock); err != nil {
		return false, err
	}
	return true, nil
}

// waitDaemonUnlocked waits until no daemon holds the lock at path, so a daemon started right after
// does not find it still taken.
func waitDaemonUnlocked(ctx context.Context, path string) error {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- airlock's own cache file.
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }() //nolint:errcheck // only read for its lock.
	for {
		err := platform.TryLockFile(lock)
		if err == nil {
			return platform.UnlockFile(lock)
		}
		if !errors.Is(err, platform.ErrLocked) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.New("the airlock daemon did not exit in time")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ensureDaemon returns a client of the daemon, starting `airlock daemon run` detached if needed.
func ensureDaemon(ctx context.Context) (*daemon.Client, error) {
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return daemon.Ensure(ctx, paths, version.String(), startDetachedDaemon, daemonLogPath())
}

func startDetachedDaemon() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return err
	}
	cmd := detachedDaemonCmd(executable, paths)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reaped once it ends, so that it never lingers as a zombie of this airlock.
	go func() { _ = cmd.Wait() }() //nolint:errcheck // its log says why it ended.
	return nil
}

// detachedDaemonCmd is `airlock daemon run`, detached, working in the daemon's directory rather
// than the starter's, which the daemon would keep in use (locked on Windows, a volume that cannot
// be ejected).
func detachedDaemonCmd(executable string, paths daemon.Paths) *exec.Cmd {
	cmd := exec.Command(executable, "daemon", "run") // #nosec G204 -- airlock's own binary.
	platform.StartDetached(cmd)
	cmd.Dir = daemonWorkDir(paths)
	return cmd
}

// daemonWorkDir is the working directory of a detached daemon: its own directory, created when
// missing, else the home directory.
func daemonWorkDir(paths daemon.Paths) string {
	if err := os.MkdirAll(paths.Dir, 0o700); err == nil {
		return paths.Dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// runDaemon is `airlock daemon run`: the daemon itself, its runner rebuilt when config.json changes.
func runDaemon(cmd *cobra.Command) error {
	log := openDaemonLog()
	defer func() { _ = log.Close() }() //nolint:errcheck // nothing to do about it.
	err := serveDaemon(cmd.Context(), log)
	switch {
	case errors.Is(err, daemon.ErrAlreadyRunning):
		// The log is the running daemon's: this one leaves it untouched.
		return fmt.Errorf("%w (see `airlock daemon status`)", err)
	case err != nil:
		// A detached daemon has no terminal: its log is where airlock looks for why it did not start.
		_, _ = fmt.Fprintf(log, "%s daemon failed: %v\n", time.Now().Format(time.RFC3339), err) //nolint:errcheck // the log is all the daemon has.
	}
	return err
}

// serveDaemon runs the daemon; its warnings only reach log once it holds the lock.
func serveDaemon(ctx context.Context, log *daemonLog) error {
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return err
	}
	configPath, err := appdir.ConfigFile()
	if err != nil {
		return err
	}
	registry := jumphost.NewSessionRegistry()
	// Like in CLI mode, a lookup failure only disables what the directory is for.
	mcpLogDir, err := appdir.LogsDir()
	log.warnLookup("MCP queries will not be logged", err)
	kubeconfigDir, err := appdir.KubeconfigDir()
	log.warnLookup("EKS sessions will not get a kubeconfig", err)
	var prefsPath string
	var databaseStore *connections.Databases
	if saved, err := openSavedConnections(log); err != nil {
		// The daemon reads the file (and migrates the old ones) only once, at its start.
		log.warnLookup("saved connections are unavailable until the file is fixed and `airlock daemon restart` is run", err)
	} else {
		prefsPath, databaseStore = saved.Path(), saved.Databases()
	}
	source := newRunnerSource(configPath, func(cfg config.Config) (sessionsRunner, error) {
		options := sharedRunnerOptions(&cfg, registry, mcpserve.NewHostFor(cfg.MCPClientSelection()), mcpLogDir, kubeconfigDir, databaseStore)
		options.Out, options.Err, options.PrefsPath, options.Keychain = log, log, prefsPath, newKeychain()
		return jumphost.NewRunner(options), nil
	})
	return daemon.Run(ctx, &daemon.RunConfig{Paths: paths, Version: version.String(), Runner: source.Source(), Log: log})
}
