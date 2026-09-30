package ui

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

// recordingTerminal records how the handoff suspends and restores the dashboard.
type recordingTerminal struct {
	calls []string
}

func (r *recordingTerminal) ReleaseTerminal() error {
	r.calls = append(r.calls, "release")
	return nil
}

func (r *recordingTerminal) RestoreTerminal() error {
	r.calls = append(r.calls, "restore")
	return nil
}

func newTestHandoff(input string) (*TerminalHandoff, *recordingTerminal, *bytes.Buffer) {
	var out bytes.Buffer
	terminal := &recordingTerminal{}
	h := &TerminalHandoff{in: strings.NewReader(input), out: &out, errOut: &out}
	h.attach(terminal)
	return h, terminal, &out
}

func TestHandoffSuspendsTheDashboardWhileTheCommandRuns(t *testing.T) {
	h, terminal, out := newTestHandoff("")

	if err := h.Run(context.Background(), "Signing in again.", "sh", "-c", "echo code ABCD-EFGH"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"release", "restore"}; !reflect.DeepEqual(terminal.calls, want) {
		t.Fatalf("calls = %v, want %v", terminal.calls, want)
	}
	for _, want := range []string{"Signing in again.", "code ABCD-EFGH"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("terminal output lacks %q:\n%s", want, out)
		}
	}
}

func TestHandoffWaitsForEnterAfterAFailure(t *testing.T) {
	h, terminal, out := newTestHandoff("\n")

	if err := h.Run(context.Background(), "Signing in again.", "sh", "-c", "echo 'Missing the SSO configuration' >&2; exit 3"); err == nil {
		t.Fatal("the failure was not returned")
	}
	if !strings.Contains(out.String(), "Missing the SSO configuration") || !strings.Contains(out.String(), "Press enter") {
		t.Fatalf("the error is not left readable before returning:\n%s", out)
	}
	if terminal.calls[len(terminal.calls)-1] != "restore" {
		t.Fatalf("calls = %v, want the dashboard restored", terminal.calls)
	}
}

func TestHandoffWithoutDashboardRunsOnTheTerminal(t *testing.T) {
	var out bytes.Buffer
	h := &TerminalHandoff{in: strings.NewReader(""), out: &out, errOut: &out}
	if err := h.Run(context.Background(), "Signing in again.", "sh", "-c", "echo done"); err != nil || !strings.Contains(out.String(), "done") {
		t.Fatalf("err = %v output = %q", err, out.String())
	}
}
