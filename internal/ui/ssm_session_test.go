package ui

import (
	"context"
	"errors"
	"fmt"
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

func (fakeSessionRunner) ListResourceSummaries(context.Context, *jumphost.Options) ([]jumphost.ResourceSummary, error) {
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

func (fakeSessionRunner) KeychainAvailable() bool { return true }

func (fakeSessionRunner) LoadKeychainPassword(context.Context, jumphost.Options) (string, bool) {
	return "", false
}

func (fakeSessionRunner) ListDBSecrets(context.Context, *jumphost.Options) ([]jumphost.SecretSummary, error) {
	return []jumphost.SecretSummary{
		{ARN: "arn:linked", Name: "prod/archive/app-user", Linked: true},
		{ARN: "arn:other", Name: "team/other"},
	}, nil
}

func (fakeSessionRunner) ListCacheIAMUsers(context.Context, *jumphost.Options) ([]string, error) {
	return []string{"app-iam"}, nil
}

func (fakeSessionRunner) SaveConnectionPrefs(context.Context, jumphost.Options) error {
	return nil
}

func (fakeSessionRunner) ListSavedConnections(context.Context, string, string) ([]jumphost.SavedConnection, error) {
	return nil, nil
}

func (fakeSessionRunner) ForgetConnection(context.Context, jumphost.TargetRef) error {
	return nil
}

func (fakeSessionRunner) SearchResources(context.Context, *jumphost.Options) ([]jumphost.ResourceSummary, map[string]error, error) {
	return []jumphost.ResourceSummary{{Name: "db", Type: "rds"}}, nil, nil
}

func (fakeSessionRunner) FindResource(_ context.Context, options *jumphost.Options, _ string) (jumphost.ResourceSummary, error) {
	return jumphost.ResourceSummary{Name: options.Name, Type: options.ResourceType}, nil
}

func (fakeSessionRunner) Prepare(_ context.Context, options jumphost.Options) (*jumphost.Session, error) {
	return &jumphost.Session{Label: options.Name, LocalPort: options.LocalPortNumber}, nil
}

func newTestSSMModel(t *testing.T, labels ...string) ssmModel {
	t.Helper()
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{})
	for idx, label := range labels {
		m = m.addSession(&jumphost.Session{Label: label, Target: label, ResourceType: "rds", Profile: "dev", Region: "eu-west-3", LocalPort: 5432 + idx}, jumphost.Options{})
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
	if isQuit(cmd) {
		t.Fatal("program quit when the last session ended: its logs must stay readable")
	}
}

func TestLastSessionEndingKeepsDashboardUntilQuit(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, cmd := update(t, m, ssmFinishedMsg{id: m.sessions[0].id, err: errors.New("TargetNotConnected")})
	if isQuit(cmd) || m.mode != ssmModeSessions {
		t.Fatalf("mode = %v quit = %v, want the dashboard kept on the ended session", m.mode, isQuit(cmd))
	}
	if !strings.Contains(m.View(), "TargetNotConnected") {
		t.Fatal("the dashboard does not show why the session ended")
	}

	_, cmd = update(t, m, key("q"))
	if !isQuit(cmd) {
		t.Fatal("q did not quit once no session runs")
	}
}

func TestStoppingLastSessionKeepsDashboard(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("enter"))
	_, cmd := update(t, m, ssmFinishedMsg{id: m.sessions[0].id})
	if isQuit(cmd) {
		t.Fatal("stopping the last session quit airlock; only q should")
	}
}

func TestStopAllAndQuitStillQuits(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, _ = update(t, m, key("q"))
	m, _ = update(t, m, key("enter"))
	_, cmd := update(t, m, ssmFinishedMsg{id: m.sessions[0].id})
	if !isQuit(cmd) {
		t.Fatal("stop all & quit did not quit once the sessions ended")
	}
}

func TestWizardCancelWithOnlyEndedSessionsReturnsToDashboard(t *testing.T) {
	m := newTestSSMModel(t, "first")
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id})

	m, _ = update(t, m, key("n"))
	if m.wizard.quitLabel() != "cancel" {
		t.Fatalf("quit label = %q, want cancel: the dashboard still shows a session", m.wizard.quitLabel())
	}
	m, cmd := update(t, m, jumphostWizardDoneMsg{canceled: true})
	if isQuit(cmd) || m.mode != ssmModeSessions {
		t.Fatalf("mode = %v quit = %v, want back to the dashboard", m.mode, isQuit(cmd))
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
	m = m.addSession(&jumphost.Session{Label: "second", LocalPort: 5433}, jumphost.Options{})
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

func TestStartupWizardOpensOnEmptyDashboard(t *testing.T) {
	base := jumphost.Options{AwsProfile: "prod", AwsRegion: "us-east-1", SkipMCP: true}
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), base).startupWizard()

	if m.mode != ssmModeWizard || !m.wizard.embedded || len(m.sessions) != 0 {
		t.Fatalf("mode = %v embedded = %v sessions = %d, want an embedded wizard on an empty dashboard", m.mode, m.wizard.embedded, len(m.sessions))
	}
	if m.wizard.askProfile || m.wizard.options.AwsProfile != "prod" || m.wizard.options.AwsRegion != "us-east-1" || !m.wizard.options.SkipMCP {
		t.Fatalf("wizard options = %+v askProfile = %v, want --profile, --region and MCP flags kept", m.wizard.options, m.wizard.askProfile)
	}
	if m.wizard.quitLabel() != "quit" {
		t.Fatalf("quit label = %q, want quit: canceling leaves airlock without sessions", m.wizard.quitLabel())
	}
}

func TestStartupWizardCancelQuits(t *testing.T) {
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{}).startupWizard()

	_, cmd := update(t, m, jumphostWizardDoneMsg{canceled: true})
	if !isQuit(cmd) {
		t.Fatal("canceling the startup wizard did not quit")
	}
}

func TestStartupWizardConnectsInDashboard(t *testing.T) {
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{}).startupWizard()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "db", LocalPortNumber: 5432}})
	if m.mode != ssmModePreparing {
		t.Fatalf("mode = %v, want preparing inside the dashboard", m.mode)
	}
	m, _ = update(t, m, preparedMsg(t, cmd))
	if m.mode != ssmModeSessions || len(m.sessions) != 1 {
		t.Fatalf("mode = %v sessions = %d, want the first session shown", m.mode, len(m.sessions))
	}
}

func TestDashboardWizardLaterIgnoresProfileFlag(t *testing.T) {
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{AwsProfile: "prod"})
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds"}, jumphost.Options{})
	m.sessions[0].state = ssmSessionRunning
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	if !m.wizard.askProfile || m.wizard.quitLabel() != "cancel" {
		t.Fatalf("askProfile = %v quit label = %q, want the profile asked and cancel back to sessions", m.wizard.askProfile, m.wizard.quitLabel())
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
	modes := []ssmViewMode{ssmModeSessions, ssmModeConfirmStop, ssmModeConfirmQuit, ssmModeHelp, ssmModeCopy}

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

func TestDetailsShowKubeconfig(t *testing.T) {
	t.Setenv("HOME", "/home/dev")
	m := newTestSSMModel(t, "platform")
	m.sessions[0].session.Details.Kubeconfig = "/home/dev/.config/airlock/kube/airlock-dev-eu-west-3-platform.json"

	// The home directory is shortened so the path fits, ready to paste in a shell.
	if view := m.View(); !strings.Contains(view, "KUBECONFIG=~/.config/airlock/kube/airlock-dev-eu-west-3-platform.json") {
		t.Fatalf("details do not show the session kubeconfig:\n%s", view)
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
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds"}, jumphost.Options{})
	m.sessions[0].state = ssmSessionRunning
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	if !m.wizard.options.SkipMCP || !m.wizard.options.MCPAllowWrites {
		t.Fatalf("wizard options = %+v, want MCP flags kept", m.wizard.options)
	}
}

// savedDBRunner remembers one connection matching the "db" session of newTestSSMModel.
type savedDBRunner struct {
	fakeSessionRunner
}

func (savedDBRunner) ListSavedConnections(context.Context, string, string) ([]jumphost.SavedConnection, error) {
	return []jumphost.SavedConnection{
		{Ref: jumphost.TargetRef{Account: "1", Region: "eu-west-3", Type: "rds", Name: "db"}, Prefs: jumphost.ConnectionPrefs{Profile: "dev"}},
		{Ref: jumphost.TargetRef{Account: "1", Region: "eu-west-3", Type: "rds", Name: "other"}, Prefs: jumphost.ConnectionPrefs{Profile: "dev"}},
	}, nil
}

func TestDashboardWizardSwitchesToConnectedEntry(t *testing.T) {
	m := newSSMModel(context.Background(), savedDBRunner{}, newProcessTracker(), jumphost.Options{})
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds", Profile: "dev", Region: "eu-west-3"}, jumphost.Options{})
	m = m.addSession(&jumphost.Session{Label: "cache", Target: "cache", ResourceType: "elasticache", Profile: "dev", Region: "eu-west-3"}, jumphost.Options{})
	m.sessions[0].state = ssmSessionRunning
	m.sessions[1].state = ssmSessionRunning
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	m, _ = update(t, m, m.wizard.loadSavedCmd()())
	if m.wizard.step != jumphostStepStart {
		t.Fatalf("step = %v, want the start screen", m.wizard.step)
	}
	item, _ := m.wizard.recent.selected()
	if !item.connected || item.conn.Ref.Name != "db" {
		t.Fatalf("item = %+v, want db marked connected", item)
	}
	if !strings.Contains(m.View(), "switch to") {
		t.Fatal("the key hints do not say that enter switches to the running session")
	}
	m, cmd := update(t, m, key("enter"))
	done, ok := wizardDone(cmd)
	if !ok {
		t.Fatal("enter on a connected entry did not leave the wizard")
	}
	m, _ = update(t, m, done)
	if m.mode != ssmModeSessions || m.selected != 0 || len(m.sessions) != 2 {
		t.Fatalf("mode = %v selected = %d sessions = %d, want the db session selected and none opened", m.mode, m.selected, len(m.sessions))
	}
}

func TestAppendWarningAddsALineToTheIntro(t *testing.T) {
	session := &jumphost.Session{Intro: []string{"RDS archive"}}
	appendWarning(session, "keychain: the password was not remembered: locked")
	if got := strings.Join(session.Intro, "\n"); got != "RDS archive\nWarning: keychain: the password was not remembered: locked" {
		t.Fatalf("intro = %q", got)
	}
	appendWarning(session, "")
	if len(session.Intro) != 2 {
		t.Fatalf("an empty warning added a line: %v", session.Intro)
	}
}

func TestStartupWizardCarriesDBSecretFlag(t *testing.T) {
	base := jumphost.Options{AwsProfile: "prod", DBSecretARN: "prod/app-user"}
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), base).startupWizard()
	if m.wizard.base.DBSecretARN != "prod/app-user" {
		t.Fatalf("wizard base = %+v, want the --db-secret flag", m.wizard.base)
	}

	// A secret belongs to one target: connections opened later from the dashboard do not inherit it.
	next, _ := m.openWizard()
	if dashboard, ok := next.(ssmModel); !ok || dashboard.wizard.base.DBSecretARN != "" {
		t.Fatalf("new connection base = %+v, want no secret", dashboard.wizard.base)
	}
}

// prepareRecorder is a session runner remembering the options of every Prepare call.
type prepareRecorder struct {
	fakeSessionRunner
	prepared *[]jumphost.Options
}

func (r prepareRecorder) Prepare(ctx context.Context, options jumphost.Options) (*jumphost.Session, error) {
	*r.prepared = append(*r.prepared, options)
	return &jumphost.Session{Label: options.Name, Target: options.Name, ResourceType: options.ResourceType, Profile: options.AwsProfile, Region: options.AwsRegion, LocalPort: options.LocalPortNumber}, nil
}

// preparedMsg runs cmd, a batch of the preparation and its spinner, and returns the preparation result.
func preparedMsg(t *testing.T, cmd tea.Cmd) ssmPreparedMsg {
	t.Helper()
	batch, ok := runWithTimeout(cmd).(tea.BatchMsg)
	if !ok {
		t.Fatal("the command is not a batch")
	}
	for _, inner := range batch {
		if msg, ok := runWithTimeout(inner).(ssmPreparedMsg); ok {
			return msg
		}
	}
	t.Fatal("no preparation in the batch")
	return ssmPreparedMsg{}
}

func newRestartModel(t *testing.T) (ssmModel, *[]jumphost.Options) {
	t.Helper()
	prepared := &[]jumphost.Options{}
	m := newSSMModel(context.Background(), prepareRecorder{prepared: prepared}, newProcessTracker(), jumphost.Options{})
	for idx, name := range []string{"archive", "cache"} {
		session := &jumphost.Session{Label: name, Target: name, ResourceType: "rds", Profile: "dev", Region: "eu-west-3", LocalPort: 5432 + idx}
		m = m.addSession(session, jumphost.Options{ResourceType: "rds", Name: name, DBUser: "app_ro", MCPAllowWrites: true})
		m.sessions[len(m.sessions)-1].state = ssmSessionRunning
	}
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, prepared
}

func TestEndedSessionRestartsInPlace(t *testing.T) {
	m, prepared := newRestartModel(t)
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id, err: errors.New("TargetNotConnected")})
	m = m.selectSession(0)
	if !strings.Contains(m.View(), "restart") {
		t.Fatal("the footer does not offer to restart the ended session")
	}

	m, cmd := update(t, m, key("r"))
	if m.mode != ssmModePreparing {
		t.Fatalf("mode = %v, want preparing the restart", m.mode)
	}
	m, _ = update(t, m, preparedMsg(t, cmd))

	want := jumphost.Options{ResourceType: "rds", Name: "archive", LocalPortNumber: 5432, AwsProfile: "dev", AwsRegion: "eu-west-3", DBUser: "app_ro", MCPAllowWrites: true}
	if len(*prepared) != 1 || (*prepared)[0] != want {
		t.Fatalf("prepared = %+v, want %+v: the same target, port and choices", *prepared, want)
	}
	if len(m.sessions) != 2 || m.selected != 0 || m.sessions[0].session.Target != "archive" || m.sessions[0].state == ssmSessionEnded {
		t.Fatalf("sessions = %d selected = %d, want archive restarted in its own slot", len(m.sessions), m.selected)
	}
	logs := m.renderLogLines(m.sessions[0], 200)
	if !strings.Contains(logs, "TargetNotConnected") || !strings.Contains(logs, "Restarting session") {
		t.Fatalf("logs = %q, want the previous run kept above the restart", logs)
	}
	if m.sessions[0].options != (*prepared)[0] {
		t.Fatal("the restarted session does not keep its options for a later restart")
	}
}

func TestRestartIgnoresRunningSession(t *testing.T) {
	m, prepared := newRestartModel(t)

	m, _ = update(t, m, key("r"))
	if m.mode != ssmModeSessions || len(*prepared) != 0 {
		t.Fatalf("mode = %v prepared = %d, a running session is not restarted", m.mode, len(*prepared))
	}
}

func TestRestartRefusesTargetConnectedElsewhere(t *testing.T) {
	m, prepared := newRestartModel(t)
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id})
	m = m.addSession(&jumphost.Session{Label: "archive", Target: "archive", ResourceType: "rds", Profile: "dev", Region: "eu-west-3", LocalPort: 5440}, jumphost.Options{})
	m.sessions[2].state = ssmSessionRunning
	m = m.selectSession(0)

	m, _ = update(t, m, key("r"))
	if m.mode != ssmModeSessions || len(*prepared) != 0 || !strings.Contains(m.errorMsg, "already connected") {
		t.Fatalf("mode = %v prepared = %d error = %q, want the duplicate refused", m.mode, len(*prepared), m.errorMsg)
	}
}

func TestRestartFailureKeepsEndedSession(t *testing.T) {
	m, _ := newRestartModel(t)
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id})
	id := m.sessions[0].id
	m.mode = ssmModePreparing

	m, _ = update(t, m, ssmPreparedMsg{err: errors.New("port 5432 is busy"), replaces: id})
	if m.mode != ssmModeSessions || len(m.sessions) != 2 || m.sessions[0].id != id || !strings.Contains(m.errorMsg, "Could not restart") {
		t.Fatalf("mode = %v sessions = %d error = %q, want the ended session kept and the error shown", m.mode, len(m.sessions), m.errorMsg)
	}
}

// wizardDone runs cmd and returns the wizard completion it leads to, if any.
func wizardDone(cmd tea.Cmd) (jumphostWizardDoneMsg, bool) {
	if cmd == nil {
		return jumphostWizardDoneMsg{}, false
	}
	done, ok := runWithTimeout(cmd).(jumphostWizardDoneMsg)
	return done, ok
}

// escFromDashboardWizard opens the wizard over a running session, waits for its first screen and presses esc.
func escFromDashboardWizard(t *testing.T, runner JumphostSessionRunner) (ssmModel, tea.Cmd) {
	t.Helper()
	m := newSSMModel(context.Background(), runner, newProcessTracker(), jumphost.Options{})
	m = m.addSession(&jumphost.Session{Label: "db", Target: "db", ResourceType: "rds", Profile: "dev", Region: "eu-west-3"}, jumphost.Options{})
	m.sessions[0].state = ssmSessionRunning
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = update(t, m, key("n"))
	m, _ = update(t, m, m.wizard.loadSavedCmd()())
	if !strings.Contains(m.View(), "esc") {
		t.Fatalf("the first wizard screen does not hint that esc goes back:\n%s", m.View())
	}
	return update(t, m, key("esc"))
}

func TestDashboardWizardEscOnStartScreenReturnsToSessions(t *testing.T) {
	m, cmd := escFromDashboardWizard(t, savedDBRunner{})
	done, ok := wizardDone(cmd)
	if !ok || !done.canceled {
		t.Fatal("esc on the start screen did not cancel the wizard")
	}
	m, _ = update(t, m, done)
	if m.mode != ssmModeSessions {
		t.Fatalf("mode = %v, want back to the dashboard", m.mode)
	}
}

func TestDashboardWizardEscOnTypeStepWithoutHistoryReturnsToSessions(t *testing.T) {
	m, cmd := escFromDashboardWizard(t, fakeSessionRunner{})
	if m.wizard.step != jumphostStepType {
		t.Fatalf("step = %v, want the Type step of a wizard without history", m.wizard.step)
	}
	if done, ok := wizardDone(cmd); !ok || !done.canceled {
		t.Fatal("esc on the first step did not cancel the wizard")
	}
}

func TestStartupWizardEscOnFirstStepStays(t *testing.T) {
	m := newSSMModel(context.Background(), fakeSessionRunner{}, newProcessTracker(), jumphost.Options{}).startupWizard()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = update(t, m, m.wizard.loadSavedCmd()())

	m, cmd := update(t, m, key("esc"))
	if _, ok := wizardDone(cmd); ok || m.wizard.step != jumphostStepType {
		t.Fatal("esc left the startup wizard: without sessions there is nowhere to go back to")
	}
}

// newLongLogModel returns a dashboard whose session log is longer than the logs panel.
func newLongLogModel(t *testing.T) ssmModel {
	t.Helper()
	m := newTestSSMModel(t, "db")
	for idx := range 200 {
		m.sessions[0].appendLines(fmt.Sprintf("line %d", idx))
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m
}

func TestLogKeysJumpToTopAndResumeFollowing(t *testing.T) {
	for _, keys := range [][2]string{{"g", "G"}, {"home", "end"}} {
		m := newLongLogModel(t)

		m, _ = update(t, m, key(keys[0]))
		if m.viewport.YOffset != 0 {
			t.Fatalf("%s: offset = %d, want the top of the log", keys[0], m.viewport.YOffset)
		}
		if view := plainView(m); !strings.Contains(view, "paused") || !strings.Contains(view, "G follow") {
			t.Fatalf("%s: a paused log does not say how to follow it again:\n%s", keys[0], view)
		}

		m, _ = update(t, m, key(keys[1]))
		if !m.viewport.AtBottom() || !strings.Contains(plainView(m), "● live") {
			t.Fatalf("%s: the log does not follow again", keys[1])
		}
	}
}

func TestStopDialogShortcuts(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, _ = update(t, m, key("x"))
	if view := plainView(m); !strings.Contains(view, "y  stop") || !strings.Contains(view, "n  cancel") {
		t.Fatalf("the dialog keys do not name y and n:\n%s", view)
	}
	m, _ = update(t, m, key("n"))
	if m.mode != ssmModeSessions || m.sessions[0].stopRequested {
		t.Fatalf("mode = %v stopped = %v, want n to keep the session", m.mode, m.sessions[0].stopRequested)
	}

	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("tab")) // focus Cancel: y still stops
	m, _ = update(t, m, key("y"))
	if m.mode != ssmModeSessions || !m.sessions[0].stopRequested {
		t.Fatalf("mode = %v stopped = %v, want y to stop the session", m.mode, m.sessions[0].stopRequested)
	}
}

func TestQuitDialogShortcuts(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, _ = update(t, m, key("q"))
	m, _ = update(t, m, key("n"))
	if m.mode != ssmModeSessions || m.quitting {
		t.Fatalf("mode = %v quitting = %v, want n to stay", m.mode, m.quitting)
	}
	m, _ = update(t, m, key("q"))
	m, _ = update(t, m, key("y"))
	if !m.quitting || !m.sessions[0].stopRequested {
		t.Fatal("y did not stop the sessions to quit")
	}
}

func TestCtrlCTwiceStopsAllAndQuits(t *testing.T) {
	m := newTestSSMModel(t, "first", "second")

	m, _ = update(t, m, key("ctrl+c"))
	if m.mode != ssmModeConfirmQuit {
		t.Fatalf("mode = %v, want the quit dialog", m.mode)
	}
	m, _ = update(t, m, key("ctrl+c"))
	if !m.quitting || !m.sessions[0].stopRequested || !m.sessions[1].stopRequested {
		t.Fatal("a second ctrl+c did not stop every session to quit")
	}
}

func TestCtrlCInStopDialogAsksToQuit(t *testing.T) {
	m := newTestSSMModel(t, "first")

	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("ctrl+c"))
	if m.mode != ssmModeConfirmQuit || m.sessions[0].stopRequested {
		t.Fatalf("mode = %v stopped = %v, want the quit dialog instead", m.mode, m.sessions[0].stopRequested)
	}
}
