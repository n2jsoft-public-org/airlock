package jumphost

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/platform"
	"golang.org/x/term"
)

// requireTunnelTools returns an error if the AWS CLI or the Session Manager plugin is not
// installed: a tunnel runs `aws ssm start-session`, which needs both.
func requireTunnelTools() error {
	if _, err := exec.LookPath("aws"); err != nil {
		return i18n.NewError("jumphost.runner.aws_cli_is_not")
	}
	if _, err := exec.LookPath("session-manager-plugin"); err != nil {
		return i18n.NewError("jumphost.runner.session_manager_plugin_is_not")
	}
	return nil
}

// RunOnHangup runs fn when the terminal is closed (SIGHUP), then lets the signal terminate the process.
// SIGINT and SIGTERM are already turned into a graceful exit by the session TUI.
// The returned function stops watching.
func RunOnHangup(fn func()) (stop func()) {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, syscall.SIGHUP)

	go func() {
		select {
		case <-signals:
			fn()
			signal.Reset(syscall.SIGHUP)
			if err := raiseSignal(syscall.SIGHUP); err != nil {
				os.Exit(1)
			}
		case <-done:
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
}

// raiseSignal sends sig to the current process. It fails on Windows, which only delivers Kill.
func raiseSignal(sig os.Signal) error {
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return self.Signal(sig)
}

func (r *Runner) runAWSStreaming(ctx context.Context, awsArgs []string, commandArgs ...string) error {
	args := append([]string{}, commandArgs...)
	args = append(args, awsArgs...)

	// Use exec.Command (not CommandContext) and start aws in its own process group,
	// so that it and its child process session-manager-plugin are both stopped
	// when the context is cancelled.
	cmd := exec.Command("aws", args...) // #nosec G204 G702 -- the aws CLI with airlock's own arguments, no shell.
	platform.StartInOwnGroup(cmd)
	cmd.Stdin = r.in
	cmd.Stdout = r.out
	cmd.Stderr = r.err

	if err := cmd.Start(); err != nil {
		return i18n.NewError("jumphost.runner.aws_failed_to_start", strings.Join(commandArgs, " "), err)
	}

	// Kill the entire process group when the context is cancelled.
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			platform.TerminateGroup(cmd)
		case <-done:
		}
	}()

	err := cmd.Wait()
	close(done)
	// A stopping group is gone, or killed, before airlock exits: it would keep the port otherwise.
	<-stopped
	// Stopped by ctx (a signal, see Run): the end the user asked for, as serveDirect.
	if err != nil && ctx.Err() == nil {
		return i18n.NewError("jumphost.runner.aws_failed", strings.Join(commandArgs, " "), err)
	}

	return nil
}

func isInteractive(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}
