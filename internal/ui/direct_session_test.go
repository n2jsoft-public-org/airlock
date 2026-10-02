package ui

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// directTestSession is a prepared direct database session, as jumphost.Runner.Prepare returns it.
func directTestSession() *jumphost.Session {
	return &jumphost.Session{
		Label:        "DIRECT legacy-billing",
		ResourceType: "direct",
		Target:       "legacy-billing",
		Details: jumphost.SessionDetails{
			ConnectedServer: "billing.db.internal:1433",
			Auth:            "password · mcp_ro",
			RestartCommand:  "airlock direct legacy-billing",
		},
	}
}

// openDirectSession hands m a prepared direct session, as after the wizard, and returns the
// command it starts.
func openDirectSession(t *testing.T, m ssmModel, session *jumphost.Session) (ssmModel, tea.Cmd) {
	t.Helper()
	m.mode = ssmModePreparing
	options := jumphost.Options{ResourceType: "direct", Name: session.Target, DBUser: "mcp_ro", DBPassword: "hunter2"}
	return update(t, m, ssmPreparedMsg{session: session, options: options, id: m.prepareID})
}

func TestDirectSessionRunsWithoutProcess(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())

	s := m.sessions[0]
	if s.state != ssmSessionRunning || !s.ready || s.cmd != nil {
		t.Fatalf("state = %v ready = %v cmd = %v, want running and ready without process", s.state, s.ready, s.cmd)
	}
	if len(m.processes.commands) != 0 {
		t.Fatalf("processes = %d, want no aws process for a direct session", len(m.processes.commands))
	}
	if !slices.ContainsFunc(s.lines, func(line ssmLogLine) bool {
		return strings.Contains(line.text, "Connected directly to billing.db.internal:1433")
	}) {
		t.Fatalf("lines = %+v, want the direct connection logged", s.lines)
	}
}

func TestDirectSessionChecksSignInAtOnce(t *testing.T) {
	session := directTestSession()
	signIn := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "password", User: "mcp_ro"}}}
	session.SignIn = signIn

	m, cmd := openDirectSession(t, newTestSSMModel(t), session)
	m = settle(t, m, cmd)

	if !slices.Equal(signIn.checked, []string{"password"}) || m.sessions[0].signIn.state != signInDone {
		t.Fatalf("checked = %v state = %v, want the password checked without waiting for a tunnel", signIn.checked, m.sessions[0].signIn.state)
	}
}

func TestStoppingDirectSessionEndsItAtOnce(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())

	m, _ = update(t, m, key("x"))
	m, cmd := update(t, m, key("enter"))

	s := m.sessions[0]
	if s.state != ssmSessionEnded || !s.stopRequested || s.reconnectPending() {
		t.Fatalf("state = %v stopRequested = %v reconnect = %v, want ended at once, never reconnected", s.state, s.stopRequested, s.reconnectPending())
	}
	if isQuit(cmd) {
		t.Fatal("stopping the session quit airlock; only q should")
	}
	if !strings.Contains(plainView(m), "Session stopped.") {
		t.Fatalf("view lacks the stop line:\n%s", plainView(m))
	}
}

func TestQuitWithDirectSessionsQuitsAtOnce(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())

	m, _ = update(t, m, key("q"))
	_, cmd := update(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatal("stop all & quit waited for a process a direct session does not have")
	}
}

func TestRestartedDirectSessionRunsAgain(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())
	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("enter"))
	ended := m.sessions[0].id

	m.mode = ssmModePreparing
	m, _ = update(t, m, ssmPreparedMsg{session: directTestSession(), replaces: ended, id: m.prepareID})

	if len(m.sessions) != 1 || m.sessions[0].state != ssmSessionRunning || m.sessions[0].cmd != nil {
		t.Fatalf("sessions = %d state = %v, want the slot reused and running without process", len(m.sessions), m.sessions[0].state)
	}
	if !strings.Contains(plainView(m), "Restarting session...") {
		t.Fatalf("view lacks the restart note:\n%s", plainView(m))
	}
}

func TestDirectSessionNeverReconnects(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())

	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id, err: errors.New("boom")})
	if m.sessions[0].reconnectPending() {
		t.Fatal("a direct session has no tunnel to reconnect")
	}
}

func TestDirectSessionShowsServerInsteadOfTunnel(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())

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
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())
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
	session := directTestSession()
	session.Production = true
	m, _ := openDirectSession(t, newTestSSMModel(t), session)

	if !m.isProduction(m.sessions[0]) || !strings.Contains(plainView(m), "PROD") {
		t.Fatalf("production = %v, want the PROD badge:\n%s", m.isProduction(m.sessions[0]), plainView(m))
	}
}

func TestIsProductionFromProfileOrFlag(t *testing.T) {
	m := newTestSSMModel(t, "archive")
	m.production = productionMatcher{"*prod*"}
	s := m.sessions[0]

	if m.isProduction(s) {
		t.Fatal("profile dev flagged as production")
	}
	s.session.Profile = "team-prod"
	if !m.isProduction(s) {
		t.Fatal("profile team-prod not flagged")
	}
	s.session.Profile, s.session.Production = "dev", true
	if !m.isProduction(s) {
		t.Fatal("the session's own production flag is ignored")
	}
}

func TestCopyMenuOfDirectSession(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())
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
	if strings.Contains(view, "Local address") || strings.Contains(view, "hunter2") {
		t.Fatalf("copy menu offers a local address or the password:\n%s", view)
	}

	m = copyChosen(t, m, "2")
	if len(*copied) != 1 || (*copied)[0] != "mcp_ro" {
		t.Fatalf("copied = %q, want the user", *copied)
	}
}

func TestSessionSummaryShowsDirectServer(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())

	var out bytes.Buffer
	m.printSessionSummary(&out)
	if !strings.Contains(out.String(), "- DIRECT legacy-billing -> billing.db.internal:1433") {
		t.Fatalf("summary = %q, want the direct server", out.String())
	}
}

func TestRejectedDirectPasswordEndsSessionAndAsksAgain(t *testing.T) {
	session := directTestSession()
	session.SignIn = &fakeSignIn{
		candidates: []jumphost.SignInCandidate{{Label: "password", User: "mcp_ro"}},
		checkErrs:  map[string]error{"password": jumphost.ErrSignInRejected},
	}
	m := newTestSSMModel(t)
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}

	m, cmd := openDirectSession(t, m, session)
	m = settle(t, m, cmd)

	s := m.sessions[0]
	if s.active() || canRetrySignIn(s) {
		t.Fatalf("state = %v, want the session ended: it holds a rejected password", s.state)
	}
	if strings.Contains(strings.Join(logTexts(s), "\n"), "The tunnel stays open") {
		t.Fatalf("lines = %v, a direct session has no tunnel", logTexts(s))
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

func logTexts(s *ssmSession) []string {
	texts := make([]string, 0, len(s.lines))
	for _, line := range s.lines {
		texts = append(texts, line.text)
	}
	return texts
}

func TestRejectedDirectPasswordKeepsAnOpenWizard(t *testing.T) {
	session := directTestSession()
	session.SignIn = &fakeSignIn{
		candidates: []jumphost.SignInCandidate{{Label: "password", User: "mcp_ro"}},
		checkErrs:  map[string]error{"password": jumphost.ErrSignInRejected},
	}
	m := newTestSSMModel(t)
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m, cmd := openDirectSession(t, m, session)
	m = m.showWizard(m.wizardDefaults())
	m.wizard.step = jumphostStepType

	m = settle(t, m, cmd)
	s := m.sessions[0]
	if s.active() || m.mode != ssmModeWizard || m.wizard.step != jumphostStepType || m.wizard.openDirect != "" {
		t.Fatalf("state = %v mode = %v step = %v, want the session ended and the open wizard untouched", s.state, m.mode, m.wizard.step)
	}
	if !slices.Contains(logTexts(s), "Press n and open legacy-billing to enter the password of mcp_ro again.") {
		t.Fatalf("lines = %v, want how to enter the password again", logTexts(s))
	}
}

func TestRejectedDirectPasswordWaitsForTheSessionsList(t *testing.T) {
	for _, mode := range []ssmViewMode{ssmModePreparing, ssmModeConfirmQuit, ssmModeCopy} {
		session := directTestSession()
		session.SignIn = &fakeSignIn{
			candidates: []jumphost.SignInCandidate{{Label: "password", User: "mcp_ro"}},
			checkErrs:  map[string]error{"password": jumphost.ErrSignInRejected},
		}
		m := newTestSSMModel(t)
		m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
		m, cmd := openDirectSession(t, m, session)
		m.mode = mode

		m = settle(t, m, cmd)
		s := m.sessions[0]
		if s.active() || m.mode != mode {
			t.Fatalf("mode %v: state = %v mode = %v, want the session ended and the screen left as it was", mode, s.state, m.mode)
		}
		if !slices.Contains(logTexts(s), "Press n and open legacy-billing to enter the password of mcp_ro again.") {
			t.Fatalf("mode %v: lines = %v, want how to enter the password again", mode, logTexts(s))
		}
	}
}

func TestQuitDialogOfDirectSessionsFreesNoPort(t *testing.T) {
	m, _ := openDirectSession(t, newTestSSMModel(t), directTestSession())

	m, _ = update(t, m, key("q"))
	if view := plainView(m); !strings.Contains(view, "This stops 1 active session.") || strings.Contains(view, "local port") {
		t.Fatalf("a direct session has no local port to free:\n%s", view)
	}

	tunnels, _ := openDirectSession(t, newTestSSMModel(t, "archive"), directTestSession())
	if body := tunnels.quitDialogBody(); body != "This stops 2 active sessions and frees the local ports of their tunnels." {
		t.Fatalf("body = %q, want the ports of the tunnels freed", body)
	}
}

func TestPreparingDirectSessionTalksNoTunnel(t *testing.T) {
	updated, _ := newTestSSMModel(t).prepare(ssmPreparedMsg{options: jumphost.Options{ResourceType: "direct", Name: "legacy-billing"}}, "Connecting...")
	m, ok := updated.(ssmModel)
	if !ok {
		t.Fatalf("prepare returned %T", updated)
	}
	for _, line := range m.loadingUI.funnyLines {
		if strings.Contains(line, "port") || strings.Contains(line, "jumphost") || strings.Contains(line, "tunnel") {
			t.Fatalf("lines = %v, a direct database has no port, jumphost or tunnel", m.loadingUI.funnyLines)
		}
	}
	if len(m.loadingUI.funnyLines) == 0 {
		t.Fatal("want lines while preparing")
	}

	updated, _ = newTestSSMModel(t).prepare(ssmPreparedMsg{options: jumphost.Options{ResourceType: "rds", Name: "archive"}}, "Connecting...")
	if rds, ok := updated.(ssmModel); !ok || !slices.Contains(rds.loadingUI.funnyLines, "Reserving a cozy local port...") {
		t.Fatalf("lines = %v, want the tunnel lines for RDS", rds.loadingUI.funnyLines)
	}
}
