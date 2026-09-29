package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

type fakeSessionRunner struct{}

func (fakeSessionRunner) ListAWSProfileSummaries(context.Context) ([]jumphost.AWSProfileSummary, error) {
	return []jumphost.AWSProfileSummary{{Name: "dev"}, {Name: "prod"}}, nil
}

func (fakeSessionRunner) ListResourceSummaries(context.Context, jumphost.Options) ([]jumphost.ResourceSummary, error) {
	return []jumphost.ResourceSummary{{Name: "db"}}, nil
}

func (fakeSessionRunner) ResolveDefaultLocalPort(context.Context, jumphost.Options) (int, error) {
	return 5432, nil
}

func (fakeSessionRunner) CheckLocalPort(int) error {
	return nil
}

// testDefaultDBUser is the default database user of fakeSessionRunner.
const testDefaultDBUser = "default_ro"

func (fakeSessionRunner) ResolveDBUser(user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return testDefaultDBUser
}

func (fakeSessionRunner) ResolveRegion(context.Context, string) string {
	return "eu-west-3"
}

func (fakeSessionRunner) LoadConnectionPrefs(context.Context, jumphost.Options) (jumphost.ConnectionPrefs, bool) {
	return jumphost.ConnectionPrefs{}, false
}

func (fakeSessionRunner) SaveConnectionPrefs(context.Context, jumphost.Options) error {
	return nil
}

func (fakeSessionRunner) Prepare(_ context.Context, options jumphost.Options) (*jumphost.Session, error) {
	return &jumphost.Session{Label: options.Name, LocalPort: options.LocalPortNumber}, nil
}

func newTestSSMModel(t *testing.T, labels ...string) ssmModel {
	t.Helper()
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{})
	for idx, label := range labels {
		m = m.addSession(&jumphost.Session{Label: label, Target: label, ResourceType: "rds", Profile: "dev", Region: "eu-west-3", LocalPort: 5432 + idx})
		m.sessions[len(m.sessions)-1].state = ssmSessionRunning
	}
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

func update(t *testing.T, m ssmModel, msg tea.Msg) (ssmModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	result, ok := updated.(ssmModel)
	if !ok {
		t.Fatalf("Update returned %T, want ssmModel", updated)
	}
	return result, cmd
}

// isQuit runs cmd and reports whether it quits; commands still waiting for events count as not quitting.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		_, ok := msg.(tea.QuitMsg)
		return ok
	case <-time.After(100 * time.Millisecond):
		return false
	}
}

func key(value string) tea.KeyMsg {
	switch value {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func TestSessionEndingKeepsOtherSessionsRunning(t *testing.T) {
	m := newTestSSMModel(t, "first", "second")

	m, cmd := update(t, m, ssmFinishedMsg{id: m.sessions[1].id, err: errors.New("boom")})
	if isQuit(cmd) {
		t.Fatal("program quit while another session is still running")
	}
	if m.sessions[0].state != ssmSessionRunning {
		t.Fatalf("first session state = %v, want running", m.sessions[0].state)
	}
	if m.sessions[1].state != ssmSessionEnded {
		t.Fatalf("second session state = %v, want ended", m.sessions[1].state)
	}

	_, cmd = update(t, m, ssmFinishedMsg{id: m.sessions[0].id})
	if !isQuit(cmd) {
		t.Fatal("program did not quit after the last session ended")
	}
}

func TestNewConnectionIsAddedNextToRunningSession(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, _ = update(t, m, key("n"))
	if m.mode != ssmModeWizard || !m.wizard.embedded {
		t.Fatalf("mode = %v embedded = %v, want embedded wizard", m.mode, m.wizard.embedded)
	}

	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "second", LocalPortNumber: 5433}})
	if m.mode != ssmModePreparing || cmd == nil {
		t.Fatalf("mode = %v, want preparing with a command", m.mode)
	}

	// Simulate the preparation result without launching aws.
	m.mode = ssmModeSessions
	m = m.addSession(&jumphost.Session{Label: "second", LocalPort: 5433})
	if len(m.sessions) != 2 || m.selected != 1 {
		t.Fatalf("sessions = %d selected = %d, want 2 sessions with the new one selected", len(m.sessions), m.selected)
	}
	if m.sessions[0].state != ssmSessionRunning {
		t.Fatalf("first session state = %v, want still running", m.sessions[0].state)
	}
}

func TestWizardCancelReturnsToSessions(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, _ = update(t, m, key("n"))
	m, cmd := update(t, m, jumphostWizardDoneMsg{canceled: true})
	if m.mode != ssmModeSessions || isQuit(cmd) {
		t.Fatalf("mode = %v quit = %v, want back to sessions", m.mode, isQuit(cmd))
	}
}

func TestPrepareFailureIsShownWithoutQuitting(t *testing.T) {
	m := newTestSSMModel(t, "first")
	m.mode = ssmModePreparing

	m, cmd := update(t, m, ssmPreparedMsg{err: errors.New("port 5432 is already used by session first")})
	if isQuit(cmd) {
		t.Fatal("program quit on a failed preparation")
	}
	if m.mode != ssmModeSessions || m.errorMsg == "" {
		t.Fatalf("mode = %v errorMsg = %q, want sessions view with error", m.mode, m.errorMsg)
	}
}

func TestTabCyclesSessions(t *testing.T) {
	m := newTestSSMModel(t, "first", "second", "third")
	m = m.selectSession(0)

	m, _ = update(t, m, key("tab"))
	if m.selected != 1 {
		t.Fatalf("selected = %d, want 1", m.selected)
	}
	m, _ = update(t, m, key("3"))
	if m.selected != 2 {
		t.Fatalf("selected = %d, want 2", m.selected)
	}
	m, _ = update(t, m, key("tab"))
	if m.selected != 0 {
		t.Fatalf("selected = %d, want wrap to 0", m.selected)
	}
}

func TestStopSelectedOnlyStopsThatSession(t *testing.T) {
	m := newTestSSMModel(t, "first", "second")
	m = m.selectSession(0)

	m, _ = update(t, m, key("x"))
	if m.mode != ssmModeConfirmStop {
		t.Fatalf("mode = %v, want stop confirmation", m.mode)
	}
	m, _ = update(t, m, key("enter"))
	if !m.sessions[0].stopRequested || m.sessions[1].stopRequested {
		t.Fatalf("stopRequested = %v/%v, want only the first", m.sessions[0].stopRequested, m.sessions[1].stopRequested)
	}
}

func TestEndedSessionCanBeClosed(t *testing.T) {
	m := newTestSSMModel(t, "first", "second")
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id, err: errors.New("boom")})
	m = m.selectSession(0)

	m, _ = update(t, m, key("x"))
	if len(m.sessions) != 1 || m.sessions[0].session.Label != "second" {
		t.Fatalf("sessions = %d, want only the running one left", len(m.sessions))
	}
}

func TestViewFitsTerminalInEveryMode(t *testing.T) {
	sizes := []tea.WindowSizeMsg{{Width: 140, Height: 40}, {Width: 70, Height: 24}}
	modes := []ssmViewMode{ssmModeSessions, ssmModeConfirmStop, ssmModeConfirmQuit}

	for _, size := range sizes {
		for _, mode := range modes {
			m := newTestSSMModel(t, "first", "second")
			m.sessions[0].session.Target = "a-very-long-database-identifier-name"
			m.sessions[0].session.ResourceType = "rds"
			m.sessions[0].appendLines(strings.Repeat("long log line ", 30))
			m.errorMsg = "port 5432 is already in use by another process"
			m.mode = mode
			m, _ = update(t, m, size)

			view := m.View()
			if got := lipgloss.Height(view); got > size.Height {
				t.Errorf("%dx%d mode %v: view height %d exceeds terminal", size.Width, size.Height, mode, got)
			}
			if got := lipgloss.Width(view); got > size.Width {
				t.Errorf("%dx%d mode %v: view width %d exceeds terminal", size.Width, size.Height, mode, got)
			}
		}
	}
}

func TestPluginOutputMarksSessionReady(t *testing.T) {
	m := newTestSSMModel(t, "first")
	m.sessions[0].ready = false
	id := m.sessions[0].id

	m, _ = update(t, m, ssmOutputMsg{id: id, line: "Waiting for connections..."})
	m, _ = update(t, m, ssmOutputMsg{id: id, line: "Connection accepted for session [abc]"})
	if !m.sessions[0].ready || m.sessions[0].connections != 1 {
		t.Fatalf("ready = %v connections = %d, want ready with 1 connection", m.sessions[0].ready, m.sessions[0].connections)
	}
}

func TestDetailsShowMCPServer(t *testing.T) {
	m := newTestSSMModel(t, "db")
	m.sessions[0].session.Details.MCP = "read-only · acme-dev-db -> http://127.0.0.1:50000/mcp/acme-dev-db-abc123 (.mcp.json)"

	if view := m.View(); !strings.Contains(view, "read-only · acme-dev-db") {
		t.Fatalf("details do not show the MCP server:\n%s", view)
	}
}

func TestDetailsShowSignInMode(t *testing.T) {
	m := newTestSSMModel(t, "db")
	m.sessions[0].session.Details.Auth = "password · acme_rw"

	if view := m.View(); !strings.Contains(view, "password · acme_rw") {
		t.Fatalf("details do not show the sign-in mode:\n%s", view)
	}
}

func TestWizardKeepsMCPOptions(t *testing.T) {
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{SkipMCP: true, MCPAllowWrites: true})
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds"})
	m.sessions[0].state = ssmSessionRunning
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	if !m.wizard.options.SkipMCP || !m.wizard.options.MCPAllowWrites {
		t.Fatalf("wizard options = %+v, want MCP flags kept", m.wizard.options)
	}
}
