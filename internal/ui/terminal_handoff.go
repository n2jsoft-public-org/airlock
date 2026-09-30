package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// suspendable is the part of a running tea.Program a handoff needs.
type suspendable interface {
	ReleaseTerminal() error
	RestoreTerminal() error
}

// TerminalHandoff runs the commands needing the user's terminal, such as aws sso login, while the
// dashboard runs: it suspends the dashboard meanwhile, so the command's prompts, URL and codes are
// readable, then restores it. Commands run one at a time.
type TerminalHandoff struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
	// terminal is the running dashboard; nil before it starts and once it ended.
	terminal suspendable
	mu       sync.Mutex
}

// NewTerminalHandoff hands the process's standard streams to the commands it runs.
func NewTerminalHandoff() *TerminalHandoff {
	return &TerminalHandoff{in: os.Stdin, out: os.Stdout, errOut: os.Stderr}
}

// attach makes Run suspend terminal; nil detaches it.
func (h *TerminalHandoff) attach(terminal suspendable) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.terminal = terminal
}

// Run explains purpose, runs name with args on the terminal and returns its error. After a
// failure it waits for enter, so the error stays readable until the dashboard comes back.
func (h *TerminalHandoff) Run(ctx context.Context, purpose, name string, args ...string) (err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminal != nil {
		if releaseErr := h.terminal.ReleaseTerminal(); releaseErr != nil {
			return fmt.Errorf("failed to hand the terminal over: %w", releaseErr)
		}
		defer func() {
			if restoreErr := h.terminal.RestoreTerminal(); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("failed to restore the dashboard: %w", restoreErr))
			}
		}()
	}

	command := strings.Join(append([]string{name}, args...), " ")
	if _, writeErr := fmt.Fprintf(h.out, "\nairlock: %s\nRunning %s; airlock comes back once it ends.\n\n", purpose, command); writeErr != nil {
		return fmt.Errorf("failed to write to the terminal: %w", writeErr)
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- commands are built by the jumphost runner.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = h.in, h.out, h.errOut
	if runErr := cmd.Run(); runErr != nil {
		return h.showFailure(command, runErr)
	}
	return nil
}

// showFailure leaves the failure of command readable until the user presses enter, and returns it.
func (h *TerminalHandoff) showFailure(command string, runErr error) error {
	if _, err := fmt.Fprintf(h.out, "\n%s failed: %v\nPress enter to return to airlock.", command, runErr); err != nil {
		return runErr
	}
	if _, err := bufio.NewReader(h.in).ReadString('\n'); err != nil && !errors.Is(err, io.EOF) {
		return errors.Join(runErr, err)
	}
	return runErr
}
