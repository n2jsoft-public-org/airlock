package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/sessions"
)

func approvalEvent() ssmEventMsg {
	return ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventApprovalRequested, Seq: 5, Approval: &sessions.Approval{
		ID: 7, Agent: "claude-code", Type: "rds", Target: "orders-db", Profile: "prod", Region: "eu-west-3",
		Reason: "check yesterday's invoices", Production: true, ExpiresAt: time.Now().Add(time.Minute),
	}}}
}

func runApprovalCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	cmd()
}

func TestApprovalDialogAllows(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	m := newDaemonView(t, service)
	m, _ = update(t, m, approvalEvent())
	if m.mode != ssmModeApproval {
		t.Fatalf("mode = %v", m.mode)
	}
	view := m.render()
	for _, want := range []string{"claude-code asks to open a connection", "orders-db", "PROD", "check yesterday", "Allow", "Deny"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog lacks %q", want)
		}
	}
	m, cmd := update(t, m, key("y"))
	runApprovalCmd(t, cmd)
	if m.mode != ssmModeSessions || !slices.Contains(service.Calls(), "approve 7") {
		t.Fatalf("mode = %v, calls = %v", m.mode, service.Calls())
	}
}

func TestApprovalDialogDeniesOnEsc(t *testing.T) {
	service := newFakeService()
	m := newDaemonView(t, service)
	m, _ = update(t, m, approvalEvent())
	_, cmd := update(t, m, key("esc"))
	runApprovalCmd(t, cmd)
	if !slices.Contains(service.Calls(), "deny 7") {
		t.Fatalf("calls = %v", service.Calls())
	}
}

func TestApprovalAnsweredElsewhereCloses(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	m, _ = update(t, m, approvalEvent())
	m, _ = update(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventApprovalResolved, Seq: 6, ApprovalID: 7, Approved: true}})
	if m.mode != ssmModeSessions || !strings.Contains(m.notice, "another view") {
		t.Fatalf("mode = %v, notice = %q", m.mode, m.notice)
	}
}

// Review Focus 5: an approval never takes the keys of the wizard or of another dialog.
func TestApprovalWaitsForTheSessionsList(t *testing.T) {
	for _, mode := range []ssmViewMode{ssmModeWizard, ssmModeSignIn, ssmModeConfirmQuit} {
		m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
		m.mode = mode
		m, _ = update(t, m, approvalEvent())
		if m.mode != mode {
			t.Fatalf("an approval took over mode %v", mode)
		}
		m.mode = ssmModeSessions
		m, _ = update(t, m, ssmTickMsg(time.Now()))
		if m.mode != ssmModeApproval {
			t.Fatalf("back on the sessions list, the dialog must open (mode %v)", m.mode)
		}
	}
}

func TestAgentSessionsAreTagged(t *testing.T) {
	s := testSession(1, "orders-db")
	s.OpenedBy = "claude-code"
	m := newDaemonView(t, newFakeService(s))
	if view := m.render(); !strings.Contains(view, "AGENT · claude-code") {
		t.Fatal("an agent session must carry AGENT · <name>")
	}
}

func TestApprovalEnterDefaultsToDenyWithoutSignIn(t *testing.T) {
	service := newFakeService()
	m := newDaemonView(t, service)
	m.interactive = func(context.Context, string, string, ...string) error { return nil }
	m, _ = update(t, m, approvalEvent())
	_, cmd := update(t, m, key("enter"))
	runApprovalCmd(t, cmd)
	if !slices.Contains(service.Calls(), "deny 7") {
		t.Fatalf("calls = %v", service.Calls())
	}
	if len(service.resolveInteractive) != 1 || service.resolveInteractive[0] {
		t.Fatalf("a deny must pass no sign-in function: %v", service.resolveInteractive)
	}
}

// Approval IDs restart with the daemon: an answer must not hide the new daemon's approval 7.
func TestApprovalIDReusedAfterReconnectOpens(t *testing.T) {
	service := newFakeService()
	m := newDaemonView(t, service)
	m, _ = update(t, m, approvalEvent())
	m, _ = update(t, m, key("n"))
	m, _ = update(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventApprovalResolved, Seq: 6, ApprovalID: 7}})
	if len(m.answered) != 0 {
		t.Fatalf("answered = %v", m.answered)
	}
	model, _ := m.loseConnection("daemon restarted")
	m = asModel(t, model)
	service.snapshot = sessions.Snapshot{Approvals: []sessions.Approval{{ID: 7, Agent: "other", Type: "rds", Target: "x-db", ExpiresAt: time.Now().Add(time.Minute)}}}
	m = m.subscribe()
	m.connLost = ""
	m = m.settleApprovals()
	if m.mode != ssmModeApproval || m.approvalID != 7 {
		t.Fatalf("mode = %v", m.mode)
	}
}

func TestApprovalAnsweredKeptUntilResolved(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	m, _ = update(t, m, approvalEvent())
	m, _ = update(t, m, key("n"))
	m, _ = update(t, m, ssmTickMsg(time.Now()))
	if m.mode != ssmModeSessions || !m.answered[7] {
		t.Fatalf("an answered approval must not reopen: mode %v", m.mode)
	}
}

// F1: the agent's text never writes control sequences to the terminal.
func TestApprovalDialogDropsControlCharacters(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	ev := approvalEvent()
	a := *ev.event.Approval
	a.Agent, a.Target, a.Profile, a.Region = "claude\x1b[2J", "orders\x1b]0;owned\a-db", "prod\x1b[1A", "eu\x1b[2K-west-3"
	a.Reason = "check \x1b[2K\x1b[1Ainvoices\nof yesterday"
	ev.event.Approval = &a
	m, _ = update(t, m, ev)
	view := m.render()
	for _, bad := range []string{"\x1b[2K", "\x1b[1A", "\x1b[2J", "\x1b]0;", "\a"} {
		if strings.Contains(view, bad) {
			t.Errorf("dialog writes %q", bad)
		}
	}
	for _, want := range []string{"invoices of yesterday", "orders]0;owned-db", "claude[2J asks"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog lacks %q:\n%s", want, view)
		}
	}
}

func TestApprovalDialogBadgesTheEnvironment(t *testing.T) {
	m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
	m.environments = mustEnvironments(t, "prod!=; staging=")
	event := approvalEvent()
	event.event.Approval.Production, event.event.Approval.Environment = false, "staging"
	m, _ = update(t, m, event)
	if view := ansi.Strip(m.render()); !strings.Contains(view, "STAGING") || strings.Contains(view, "PROD") {
		t.Fatalf("dialog = %s, want a STAGING badge", view)
	}
}
