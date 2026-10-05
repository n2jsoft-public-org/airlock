package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/n2jsoft/airlock/internal/platform"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// ErrAlreadyRunning reports another daemon holding the lock.
var ErrAlreadyRunning = errors.New("an airlock daemon is already running")

// defaultIdleTimeout stops a daemon without running session nor view.
const defaultIdleTimeout = 10 * time.Minute

// RunConfig is what the daemon runs with; internal/cli builds it from config.json.
type RunConfig struct {
	Log         io.Writer
	Runner      sessions.RunnerSource
	Launch      sessions.Launcher
	Paths       Paths
	Version     string
	IdleTimeout time.Duration
	// Gateway starts the MCP gateway; nil runs without one.
	Gateway StartGatewayFunc
}

// Run is the daemon: it serves the sessions until ctx ends, SIGTERM/SIGINT, daemon.stop or idleness.
func Run(ctx context.Context, cfg *RunConfig) error {
	logf := func(format string, args ...any) {
		if cfg.Log != nil {
			_, err := fmt.Fprintf(cfg.Log, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
			discard(err)
		}
	}
	if err := os.MkdirAll(cfg.Paths.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.Paths.Dir, 0o700); err != nil { // #nosec G302 -- a directory needs the execute bit.
		return fmt.Errorf("failed to make the daemon directory %s private: %w", cfg.Paths.Dir, err)
	}
	if err := checkPrivateDir(cfg.Paths.Dir); err != nil {
		return err
	}
	lock, err := os.OpenFile(cfg.Paths.Lock, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- airlock's own cache file.
	if err != nil {
		return err
	}
	defer func() { discard(lock.Close()) }()
	if lockErr := platform.TryLockFile(lock); lockErr != nil {
		if errors.Is(lockErr, platform.ErrLocked) {
			return ErrAlreadyRunning
		}
		return lockErr
	}
	defer func() { discard(platform.UnlockFile(lock)) }()

	// The lock is ours: a socket or processes left behind belong to a dead daemon.
	discard(os.Remove(cfg.Paths.Socket))
	store := newProcStore(cfg.Paths.Processes)
	if killed := store.killLeftovers(); killed > 0 {
		logf("stopped %d aws processes left by a previous daemon", killed)
	}
	listener, err := net.Listen("unix", cfg.Paths.Socket)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", cfg.Paths.Socket, err)
	}
	defer func() { discard(os.Remove(cfg.Paths.Socket)) }()
	if err := restrictSocket(cfg.Paths.Socket); err != nil {
		discard(listener.Close())
		return fmt.Errorf("failed to make the daemon socket %s private: %w", cfg.Paths.Socket, err)
	}

	platform.IgnoreHangup()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	launch := cfg.Launch
	if launch == nil {
		launch = sessions.AWSLauncher
	}
	manager := sessions.NewManager(context.WithoutCancel(ctx), sessions.Config{Runner: cfg.Runner, Launch: store.tracking(launch)})
	idle := newIdleWatch(cfg.IdleTimeout, cancel)
	gateway := &gatewayState{off: "off"}
	// An explicit stop (daemon.stop) removes the gateway entry; a stop on its own (idle, signal)
	// keeps it, still valid for the next start.
	closeGateway := func(unregister bool) {
		if err := gateway.close(unregister); err != nil {
			logf("MCP gateway stopped with errors: %v", err)
		}
	}
	server := NewServer(manager, cfg.Version, ServerHooks{
		StopDaemon: cancel,
		BeforeStop: func() { closeGateway(true) },
		ViewsChanged: func(views int) {
			idle.views(views)
			manager.ViewsChanged(views)
		},
		GatewayStatus: gateway.status,
	})
	go idle.watch(ctx, manager)
	go func() { discard(server.Serve(listener)) }()
	logf("daemon %s started (pid %d)", cfg.Version, os.Getpid())
	if cfg.Gateway != nil {
		started, err := cfg.Gateway(ctx, GatewayEnv{Manager: manager, Activity: idle.activity, Log: logf})
		switch {
		case err != nil:
			gateway.set(nil, "off: "+err.Error())
			logf("MCP gateway not started: %v", err)
		case started != nil:
			gateway.set(started, "")
			logf("MCP gateway started: %s", started.StatusWithoutToken())
		}
	}

	<-ctx.Done()
	logf("daemon stopping")
	// First, so the gateway opens no session while they stop (daemon.stop already closed it, and
	// removed its entry, through BeforeStop; closing twice is a no-op).
	closeGateway(false)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), stopAllTimeout)
	defer stopCancel()
	discard(manager.StopAll(stopCtx))
	manager.Shutdown()
	server.Close("daemon stopped")
	return nil
}

// idleWatch cancels the daemon once it had neither session nor view for its timeout.
type idleWatch struct {
	cancel     context.CancelFunc
	viewsCh    chan int
	activityCh chan struct{}
	timeout    time.Duration
}

func newIdleWatch(timeout time.Duration, cancel context.CancelFunc) *idleWatch {
	if timeout <= 0 {
		timeout = defaultIdleTimeout
	}
	return &idleWatch{timeout: timeout, cancel: cancel, viewsCh: make(chan int, 16), activityCh: make(chan struct{}, 1)}
}

// views never blocks: it runs under the server's lock.
func (w *idleWatch) views(n int) {
	select {
	case w.viewsCh <- n:
	default:
	}
}

// activity tells that the gateway served a request; it never blocks.
func (w *idleWatch) activity() {
	select {
	case w.activityCh <- struct{}{}:
	default:
	}
}

// snapshotter is the part of the sessions the idle watch reads.
type snapshotter interface {
	Snapshot(ctx context.Context) (sessions.Snapshot, error)
}

// watch counts the running sessions, the views and the gateway requests: an ended session (a
// reconnection that gave up) goes away with the daemon.
func (w *idleWatch) watch(ctx context.Context, manager snapshotter) {
	tick := time.NewTicker(min(w.timeout/4, time.Minute))
	defer tick.Stop()
	views, idleSince := 0, time.Now()
	for {
		active := false
		select {
		case <-ctx.Done():
			return
		case views = <-w.viewsCh:
		case <-w.activityCh:
			active = true
		case <-tick.C:
		}
		snapshot, err := manager.Snapshot(ctx)
		discard(err)
		if active || views > 0 || hasActiveSession(snapshot.Sessions) {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) >= w.timeout {
			w.cancel()
			return
		}
	}
}

func hasActiveSession(list []sessions.Session) bool {
	for index := range list {
		if list[index].Active() {
			return true
		}
	}
	return false
}
