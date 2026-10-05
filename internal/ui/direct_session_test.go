package ui

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// directTestSession is a running direct database session, as the service shows it.
func directTestSession() sessions.Session {
	return sessions.Session{
		ID:           1,
		Label:        "DIRECT legacy-billing",
		ResourceType: sessions.DirectResourceType,
		Target:       "legacy-billing",
		State:        sessions.StateRunning,
		Ready:        true,
		Options:      jumphost.Options{ResourceType: sessions.DirectResourceType, Name: "legacy-billing", DBUser: "mcp_ro"},
		Details: jumphost.SessionDetails{
			ConnectedServer: "billing.db.internal:1433",
			Auth:            "password · mcp_ro",
			RestartCommand:  "airlock direct legacy-billing",
		},
		Lines: []sessions.LogLine{{Text: "Connected directly to billing.db.internal:1433: no tunnel to wait for."}},
	}
}

// newDirectModel returns a dashboard showing list, the direct session selected.
func newDirectModel(t *testing.T, list ...sessions.Session) ssmModel {
	t.Helper()
	m := newServiceModel(t, newFakeService(append(list, directTestSession())...))
	return m.selectSession(len(list))
}

func TestDirectSessionShowsServerInsteadOfTunnel(t *testing.T) {
	m := newDirectModel(t)

	view := plainView(m)
	for _, want := range []string{"DIRECT", "legacy-billing", "billing.db.internal:1433", "Server", "direct · "} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"localhost:0", "Local ", "Remote "} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("view shows %q, which a direct session does not have:\n%s", unwanted, view)
		}
	}
}

func TestDirectSessionCompactViewShowsNoLocalPort(t *testing.T) {
	m := newDirectModel(t)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})

	view := plainView(m)
	if !strings.Contains(view, "legacy-billing") {
		t.Fatalf("compact view lacks the session:\n%s", view)
	}
	if strings.Contains(view, "legacy-billing :0") {
		t.Fatalf("compact view shows a local port:\n%s", view)
	}
}

func TestProductionDirectSessionIsFlagged(t *testing.T) {
	s := directTestSession()
	s.Production = true
	m := newServiceModel(t, newFakeService(s))

	if !m.isProduction(&m.sessions[0]) || !strings.Contains(plainView(m), "PROD") {
		t.Fatalf("production = %v, want the PROD badge:\n%s", m.isProduction(&m.sessions[0]), plainView(m))
	}
}

func TestIsProductionFromProfileOrFlag(t *testing.T) {
	m, _ := newSessionsModel(t, "archive")
	m.production = productionMatcher{"*prod*"}
	s := &m.sessions[0]

	if m.isProduction(s) {
		t.Fatal("profile dev flagged as production")
	}
	s.Profile = "team-prod"
	if !m.isProduction(s) {
		t.Fatal("profile team-prod not flagged")
	}
	s.Profile, s.Production = "dev", true
	if !m.isProduction(s) {
		t.Fatal("the session's own production flag is ignored")
	}
}

func TestCopyMenuOfDirectSession(t *testing.T) {
	m := newDirectModel(t)
	copied := &[]string{}
	m.copier = func(text string) error {
		*copied = append(*copied, text)
		return nil
	}

	m, _ = update(t, m, key("c"))
	view := plainView(m)
	for _, want := range []string{"Server address", "billing.db.internal:1433", "User", "mcp_ro", "Reconnect command", "airlock direct legacy-billing"} {
		if !strings.Contains(view, want) {
			t.Fatalf("copy menu lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Local address") {
		t.Fatalf("copy menu offers a local address:\n%s", view)
	}

	m = copyChosen(t, m, "2")
	if len(*copied) != 1 || (*copied)[0] != "mcp_ro" {
		t.Fatalf("copied = %q, want the user", *copied)
	}
}

func TestSessionSummaryShowsDirectServer(t *testing.T) {
	s := directTestSession()
	s.Cleanup = "MCP server legacy-billing removed from .mcp.json"

	var out bytes.Buffer
	printSessionSummary(&out, []sessions.Session{s})
	for _, want := range []string{"- DIRECT legacy-billing -> billing.db.internal:1433", "  MCP server legacy-billing removed from .mcp.json", "new session command: airlock direct legacy-billing"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary = %q, want %q", out.String(), want)
		}
	}
}

// rejectPassword feeds m the end of the direct session, whose password the database rejected.
func rejectPassword(t *testing.T, m ssmModel) ssmModel {
	t.Helper()
	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) {
		s.State, s.Err, s.SignIn.State = sessions.StateEnded, jumphost.ErrSignInRejected.Error(), sessions.SignInFailed
	})
	m, _ = sendEvent(t, m, sessions.Event{Kind: sessions.EventPasswordRejected, ID: 1, Name: "legacy-billing", User: "mcp_ro", Err: jumphost.ErrSignInRejected.Error()})
	return m
}

func TestRejectedDirectPasswordEndsSessionAndAsksAgain(t *testing.T) {
	m := newDirectModel(t)
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}

	m = rejectPassword(t, m)
	s := m.selectedSession()
	if s == nil {
		t.Fatal("no session selected")
	}
	if s.Active() || canRetrySignIn(s) {
		t.Fatalf("state = %v, want the session ended: it holds a rejected password", s.State)
	}
	if m.mode != ssmModeWizard {
		t.Fatalf("mode = %v, want the wizard asking the password again", m.mode)
	}

	m, _ = update(t, m, jumphostSavedLoadedMsg{})
	w := m.wizard
	if w.step != jumphostStepReview || !w.isDirect() || w.directDB.Name != "legacy-billing" || w.passwordInput.Value() != "" {
		t.Fatalf("step = %v db = %q password = %q, want the review of legacy-billing with an empty password", w.step, w.directDB.Name, w.passwordInput.Value())
	}
	if view := wizardView(w); !strings.Contains(view, "rejected the password of mcp_ro") {
		t.Fatalf("view lacks the rejection:\n%s", view)
	}
}

func TestRejectedDirectPasswordKeepsAnOpenWizard(t *testing.T) {
	m := newDirectModel(t)
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m = m.showWizard(m.wizardDefaults())
	m.wizard.step = jumphostStepType

	m = rejectPassword(t, m)
	if m.mode != ssmModeWizard || m.wizard.step != jumphostStepType || m.wizard.openDirect != "" {
		t.Fatalf("mode = %v step = %v, want the open wizard untouched", m.mode, m.wizard.step)
	}
}

func TestRejectedDirectPasswordWaitsForTheSessionsList(t *testing.T) {
	for _, mode := range []ssmViewMode{ssmModePreparing, ssmModeConfirmQuit, ssmModeCopy} {
		m := newDirectModel(t)
		m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
		m.mode = mode

		if m = rejectPassword(t, m); m.mode != mode {
			t.Fatalf("mode %v: mode = %v, want the screen left as it was", mode, m.mode)
		}
	}

	// Nor does it take over another session.
	m := newDirectModel(t, testSession(2, "archive"))
	m = m.selectSession(0)
	if m = rejectPassword(t, m); m.mode != ssmModeSessions {
		t.Fatalf("mode = %v, want the selected archive session kept", m.mode)
	}
}

func TestQuitDialogOfDirectSessionsFreesNoPort(t *testing.T) {
	m := newDirectModel(t)

	m, _ = update(t, m, key("q"))
	if view := plainView(m); !strings.Contains(view, "This stops 1 active session.") || strings.Contains(view, "local port") {
		t.Fatalf("a direct session has no local port to free:\n%s", view)
	}

	tunnels := newDirectModel(t, testSession(2, "archive"))
	if body := tunnels.quitDialogBody(); body != "This stops 2 active sessions and frees the local ports of their tunnels." {
		t.Fatalf("body = %q, want the ports of the tunnels freed", body)
	}
}

func TestPreparingDirectSessionTalksNoTunnel(t *testing.T) {
	updated, _ := newServiceModel(t, newFakeService()).open(jumphost.Options{ResourceType: "direct", Name: "legacy-billing"}, "", "Connecting...")
	m, ok := updated.(ssmModel)
	if !ok {
		t.Fatalf("open returned %T", updated)
	}
	for _, line := range m.loadingUI.funnyLines {
		if strings.Contains(line, "port") || strings.Contains(line, "jumphost") || strings.Contains(line, "tunnel") {
			t.Fatalf("lines = %v, a direct database has no port, jumphost or tunnel", m.loadingUI.funnyLines)
		}
	}
	if len(m.loadingUI.funnyLines) == 0 {
		t.Fatal("want lines while preparing")
	}

	updated, _ = newServiceModel(t, newFakeService()).open(jumphost.Options{ResourceType: "rds", Name: "archive"}, "", "Connecting...")
	if rds, ok := updated.(ssmModel); !ok || !slices.Contains(rds.loadingUI.funnyLines, "Reserving a cozy local port...") {
		t.Fatalf("lines = %v, want the tunnel lines for RDS", rds.loadingUI.funnyLines)
	}
}
