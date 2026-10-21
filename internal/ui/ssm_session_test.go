package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/dbuser"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
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

// testDefaultDBUser and testDefaultSQLServerUser are the default database users of
// fakeSessionRunner: the AWS IAM user of PostgreSQL and MySQL, the user of SQL Server.
const (
	testDefaultDBUser        = "default_ro"
	testDefaultSQLServerUser = "mssql_ro"
)

func (fakeSessionRunner) ResolveDBUser(engine, user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return dbuser.Defaults{IAM: testDefaultDBUser, SQLServer: testDefaultSQLServerUser}.For(engine)
}

func (fakeSessionRunner) ResolveRegion(context.Context, string) string {
	return "eu-west-3"
}

func (fakeSessionRunner) LoadConnectionPrefs(context.Context, jumphost.Options) (jumphost.ConnectionPrefs, bool) {
	return jumphost.ConnectionPrefs{}, false
}

func (fakeSessionRunner) KeychainAvailable() bool { return true }

func (fakeSessionRunner) ModuleDisabled(string) bool { return false }

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

func TestSessionEndingKeepsOtherSessionsRunning(t *testing.T) {
	m, _ := newSessionsModel(t, "first", "second")

	m, cmd := sendUpdate(t, m, 2, ended("boom"))
	if isQuit(cmd) {
		t.Fatal("program quit while another session is still running")
	}
	_, cmd = sendUpdate(t, m, 1, ended(""))
	if isQuit(cmd) {
		t.Fatal("program quit when the last session ended: its logs must stay readable")
	}
}

func TestLastSessionEndingKeepsDashboardUntilQuit(t *testing.T) {
	m, _ := newSessionsModel(t, "first")

	m, cmd := sendUpdate(t, m, 1, ended("TargetNotConnected"))
	if isQuit(cmd) || m.mode != ssmModeSessions {
		t.Fatalf("mode = %v quit = %v, want the dashboard kept on the ended session", m.mode, isQuit(cmd))
	}
	if !strings.Contains(m.render(), "TargetNotConnected") {
		t.Fatal("the dashboard does not show why the session ended")
	}

	_, cmd = update(t, m, key("q"))
	if !isQuit(cmd) {
		t.Fatal("q did not quit once no session runs")
	}
}

func TestStoppingLastSessionKeepsDashboard(t *testing.T) {
	m, _ := newSessionsModel(t, "first")

	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("enter"))
	_, cmd := sendUpdate(t, m, 1, ended(""))
	if isQuit(cmd) {
		t.Fatal("stopping the last session quit airlock; only q should")
	}
}

func TestStopAllAndQuitStillQuits(t *testing.T) {
	m, service := newSessionsModel(t, "first")

	m, _ = update(t, m, key("q"))
	m, cmd := update(t, m, key("enter"))
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "stop-all" {
		t.Fatalf("calls = %v, want every session stopped", calls)
	}
	_, cmd = sendUpdate(t, m, 1, ended(""))
	if !isQuit(cmd) {
		t.Fatal("stop all & quit did not quit once the sessions ended")
	}
}

func TestWizardCancelWithOnlyEndedSessionsReturnsToDashboard(t *testing.T) {
	m, _ := newSessionsModel(t, "first")
	m, _ = sendUpdate(t, m, 1, ended(""))

	m, _ = update(t, m, key("n"))
	if m.wizard.quitLabel() != "cancel" {
		t.Fatalf("quit label = %q, want cancel: the dashboard still shows a session", m.wizard.quitLabel())
	}
	m, cmd := update(t, m, jumphostWizardDoneMsg{canceled: true})
	if isQuit(cmd) || m.mode != ssmModeSessions {
		t.Fatalf("mode = %v quit = %v, want back to the dashboard", m.mode, isQuit(cmd))
	}
}

// openedMsg runs cmd, a batch of the opening and its spinner, and returns the opening result.
func openedMsg(t *testing.T, cmd tea.Cmd) ssmOpenedMsg {
	t.Helper()
	batch, ok := runWithTimeout(cmd).(tea.BatchMsg)
	if !ok {
		t.Fatal("the command is not a batch")
	}
	for _, inner := range batch {
		if msg, ok := runWithTimeout(inner).(ssmOpenedMsg); ok {
			return msg
		}
	}
	t.Fatal("no opening in the batch")
	return ssmOpenedMsg{}
}

// added is the EventAdded of a new running session id named target.
func added(id sessions.ID, target string) sessions.Event {
	return sessions.Event{Kind: sessions.EventAdded, ID: id, Session: new(testSession(id, target))}
}

func TestNewConnectionIsAddedNextToRunningSession(t *testing.T) {
	m, service := newSessionsModel(t, "first")

	m, _ = update(t, m, key("n"))
	if m.mode != ssmModeWizard || !m.wizard.embedded {
		t.Fatalf("mode = %v embedded = %v, want embedded wizard", m.mode, m.wizard.embedded)
	}
	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "second", LocalPortNumber: 5433}})
	if m.mode != ssmModePreparing || cmd == nil {
		t.Fatalf("mode = %v, want preparing with a command", m.mode)
	}
	opened := openedMsg(t, cmd)
	if len(service.opened) != 1 || service.opened[0].Options.Name != "second" || opened.id != 101 {
		t.Fatalf("opened = %+v, id = %d, want second asked of the service", service.opened, opened.id)
	}

	// The opening's answer may come before its session: the session is selected once it arrives.
	m, _ = update(t, m, opened)
	if m.mode != ssmModeSessions || m.selected != 0 {
		t.Fatalf("mode = %v selected = %d, want the sessions, first still selected", m.mode, m.selected)
	}
	m, _ = sendEvent(t, m, added(101, "second"))
	if len(m.sessions) != 2 || m.selected != 1 || m.sessions[0].State != sessions.StateRunning {
		t.Fatalf("sessions = %d selected = %d, want 2 sessions with the new one selected", len(m.sessions), m.selected)
	}
}

func TestNewConnectionArrivingBeforeItsAnswerIsSelected(t *testing.T) {
	m, _ := newSessionsModel(t, "first")
	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "second"}})
	opened := openedMsg(t, cmd)

	m, _ = sendEvent(t, m, added(opened.id, "second"))
	if m.mode != ssmModePreparing || m.selected != 0 {
		t.Fatalf("mode = %v selected = %d, want the preparation shown until its answer", m.mode, m.selected)
	}
	m, _ = update(t, m, opened)
	if m.mode != ssmModeSessions || m.selected != 1 {
		t.Fatalf("mode = %v selected = %d, want the new session selected", m.mode, m.selected)
	}
}

func TestWizardCancelReturnsToSessions(t *testing.T) {
	m, _ := newSessionsModel(t, "first")

	m, _ = update(t, m, key("n"))
	m, cmd := update(t, m, jumphostWizardDoneMsg{canceled: true})
	if m.mode != ssmModeSessions || isQuit(cmd) {
		t.Fatalf("mode = %v quit = %v, want back to sessions", m.mode, isQuit(cmd))
	}
}

func TestStartupWizardOpensOnEmptyDashboard(t *testing.T) {
	base := jumphost.Options{AwsProfile: "prod", AwsRegion: "us-east-1", SkipMCP: true}
	m := newSSMModel(t.Context(), newFakeService(), fakeSessionRunner{}, base).subscribe().startupWizard()

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
	m := newServiceModel(t, newFakeService()).startupWizard()

	_, cmd := update(t, m, jumphostWizardDoneMsg{canceled: true})
	if !isQuit(cmd) {
		t.Fatal("canceling the startup wizard did not quit")
	}
}

func TestStartupWizardConnectsInDashboard(t *testing.T) {
	m := newServiceModel(t, newFakeService()).startupWizard()

	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "db", LocalPortNumber: 5432}})
	if m.mode != ssmModePreparing {
		t.Fatalf("mode = %v, want preparing inside the dashboard", m.mode)
	}
	opened := openedMsg(t, cmd)
	m, _ = update(t, m, opened)
	m, _ = sendEvent(t, m, added(opened.id, "db"))
	if s := m.selectedSession(); m.mode != ssmModeSessions || len(m.sessions) != 1 || s == nil || s.Target != "db" {
		t.Fatalf("mode = %v sessions = %d, want the first session shown", m.mode, len(m.sessions))
	}
}

func TestDashboardWizardLaterIgnoresProfileFlag(t *testing.T) {
	m := newSSMModel(t.Context(), newFakeService(testSession(1, "db")), fakeSessionRunner{}, jumphost.Options{AwsProfile: "prod"}).subscribe()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	if !m.wizard.askProfile || m.wizard.quitLabel() != "cancel" {
		t.Fatalf("askProfile = %v quit label = %q, want the profile asked and cancel back to sessions", m.wizard.askProfile, m.wizard.quitLabel())
	}
}

func TestPrepareFailureIsShownWithoutQuitting(t *testing.T) {
	m, service := newSessionsModel(t, "first")
	service.openErr = errors.New("port 5432 is already used by session first")

	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "second"}})
	m, cmd = update(t, m, openedMsg(t, cmd))
	if isQuit(cmd) {
		t.Fatal("program quit on a failed preparation")
	}
	if m.mode != ssmModeSessions || m.errorMsg != "Could not open connection: port 5432 is already used by session first" {
		t.Fatalf("mode = %v errorMsg = %q, want sessions view with error", m.mode, m.errorMsg)
	}
}

func TestOpeningPassesTheWarningToTheService(t *testing.T) {
	m, service := newSessionsModel(t, "first")
	_, cmd := update(t, m, jumphostWizardDoneMsg{
		options: jumphost.Options{ResourceType: "rds", Name: "second"},
		warning: "keychain: the password was not remembered: locked",
	})
	openedMsg(t, cmd)
	if len(service.opened) != 1 || service.opened[0].Warning != "keychain: the password was not remembered: locked" {
		t.Fatalf("opened = %+v, want the wizard's warning sent with the opening", service.opened)
	}
}

func TestTabCyclesSessions(t *testing.T) {
	m, _ := newSessionsModel(t, "first", "second", "third")
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
	m, service := newSessionsModel(t, "first", "second")
	m = m.selectSession(0)

	m, _ = update(t, m, key("x"))
	if m.mode != ssmModeConfirmStop {
		t.Fatalf("mode = %v, want stop confirmation", m.mode)
	}
	_, cmd := update(t, m, key("enter"))
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "stop 1" {
		t.Fatalf("calls = %v, want only the first stopped", calls)
	}
}

func TestEndedSessionCanBeClosed(t *testing.T) {
	m, service := newSessionsModel(t, "first", "second")
	m, _ = sendUpdate(t, m, 1, ended("boom"))
	m = m.selectSession(0)

	m, cmd := update(t, m, key("x"))
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "remove 1" {
		t.Fatalf("calls = %v, want the ended session closed", calls)
	}
	m, _ = sendEvent(t, m, sessions.Event{Kind: sessions.EventRemoved, ID: 1})
	if len(m.sessions) != 1 || m.sessions[0].Target != "second" || m.selected != 0 {
		t.Fatalf("sessions = %d selected = %d, want only the running one left, selected", len(m.sessions), m.selected)
	}
}

func TestCloseFailureIsShown(t *testing.T) {
	m, _ := newSessionsModel(t, "first")
	m, _ = update(t, m, ssmActionMsg{prefix: "Could not close: ", err: errors.New("first is still running: stop it first")})
	if !strings.Contains(plainView(m), "Could not close: first is still running") {
		t.Fatalf("the refused request is not shown:\n%s", plainView(m))
	}
}

func TestViewFitsTerminalInEveryMode(t *testing.T) {
	sizes := []tea.WindowSizeMsg{{Width: 140, Height: 40}, {Width: 70, Height: 24}}
	modes := []ssmViewMode{ssmModeSessions, ssmModeConfirmStop, ssmModeConfirmQuit, ssmModeHelp, ssmModeCopy}

	for _, size := range sizes {
		for _, mode := range modes {
			long := testSession(1, "a-very-long-database-identifier-name")
			long.Lines = append(long.Lines, sessions.LogLine{Text: strings.Repeat("long log line ", 30)})
			m := newServiceModel(t, newFakeService(long, testSession(2, "second")))
			m.errorMsg = "port 5432 is already in use by another process"
			m.mode = mode
			m, _ = update(t, m, size)

			view := m.render()
			if got := lipgloss.Height(view); got > size.Height {
				t.Errorf("%dx%d mode %v: view height %d exceeds terminal", size.Width, size.Height, mode, got)
			}
			if got := lipgloss.Width(view); got > size.Width {
				t.Errorf("%dx%d mode %v: view width %d exceeds terminal", size.Width, size.Height, mode, got)
			}
		}
	}
}

// detailsModel returns a dashboard showing one session named target, its details set by change.
func detailsModel(t *testing.T, target string, change func(d *jumphost.SessionDetails)) ssmModel {
	t.Helper()
	s := testSession(1, target)
	change(&s.Details)
	return newServiceModel(t, newFakeService(s))
}

func TestDetailsShowMCPServer(t *testing.T) {
	m := detailsModel(t, "db", func(d *jumphost.SessionDetails) {
		d.MCP = "read-only · acme-dev-db -> http://127.0.0.1:50000/mcp/acme-dev-db-abc123 (.mcp.json)"
	})
	if view := m.render(); !strings.Contains(view, "read-only · acme-dev-db") {
		t.Fatalf("details do not show the MCP server:\n%s", view)
	}
}

func TestDetailsShowKubeconfig(t *testing.T) {
	t.Setenv("HOME", "/home/dev")
	m := detailsModel(t, "platform", func(d *jumphost.SessionDetails) {
		d.Kubeconfig = "/home/dev/.config/airlock/kube/airlock-dev-eu-west-3-platform.json"
	})
	// The home directory is shortened so the path fits, ready to paste in a shell.
	if view := m.render(); !strings.Contains(view, "KUBECONFIG=~/.config/airlock/kube/airlock-dev-eu-west-3-platform.json") {
		t.Fatalf("details do not show the session kubeconfig:\n%s", view)
	}
}

func TestDetailsShowSignInMode(t *testing.T) {
	m := detailsModel(t, "db", func(d *jumphost.SessionDetails) { d.Auth = "password · acme_rw" })
	if view := m.render(); !strings.Contains(view, "password · acme_rw") {
		t.Fatalf("details do not show the sign-in mode:\n%s", view)
	}
}

func TestWizardKeepsMCPOptions(t *testing.T) {
	m := newSSMModel(t.Context(), newFakeService(testSession(1, "db")), fakeSessionRunner{}, jumphost.Options{SkipMCP: true, MCPAllowWrites: true}).subscribe()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = update(t, m, key("n"))
	if !m.wizard.options.SkipMCP || !m.wizard.options.MCPAllowWrites {
		t.Fatalf("wizard options = %+v, want MCP flags kept", m.wizard.options)
	}
}

// savedDBRunner remembers one connection matching the "db" session of testSession.
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
	cache := testSession(2, "cache")
	cache.ResourceType = "elasticache"
	m := newSSMModel(t.Context(), newFakeService(testSession(1, "db"), cache), savedDBRunner{}, jumphost.Options{}).subscribe()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = m.selectSession(1)

	m, _ = update(t, m, key("n"))
	m, _ = update(t, m, m.wizard.loadSavedCmd()())
	if m.wizard.step != jumphostStepStart {
		t.Fatalf("step = %v, want the start screen", m.wizard.step)
	}
	item, _ := m.wizard.recent.selected()
	if !item.connected || item.conn.Ref.Name != "db" {
		t.Fatalf("item = %+v, want db marked connected", item)
	}
	if !strings.Contains(m.render(), "switch to") {
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

func TestStartupWizardCarriesDBSecretFlag(t *testing.T) {
	base := jumphost.Options{AwsProfile: "prod", DBSecretARN: "prod/app-user"}
	m := newSSMModel(t.Context(), newFakeService(), fakeSessionRunner{}, base).startupWizard()
	if m.wizard.base.DBSecretARN != "prod/app-user" {
		t.Fatalf("wizard base = %+v, want the --db-secret flag", m.wizard.base)
	}

	// A secret belongs to one target: connections opened later from the dashboard do not inherit it.
	next, _ := m.openWizard()
	if dashboard, ok := next.(ssmModel); !ok || dashboard.wizard.base.DBSecretARN != "" {
		t.Fatalf("new connection base = %+v, want no secret", dashboard.wizard.base)
	}
}

func TestEndedSessionRestartsInPlace(t *testing.T) {
	m, service := newSessionsModel(t, "archive", "cache")
	m, _ = sendUpdate(t, m, 1, ended("TargetNotConnected"))
	m = m.selectSession(0)
	if !strings.Contains(m.render(), "restart") {
		t.Fatal("the footer does not offer to restart the ended session")
	}

	m, cmd := update(t, m, key("r"))
	if m.mode != ssmModePreparing || !strings.Contains(plainView(m), "Restarting RDS archive...") {
		t.Fatalf("mode = %v, want preparing the restart:\n%s", m.mode, plainView(m))
	}
	m, _ = update(t, m, openedMsg(t, cmd))
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "restart 1" {
		t.Fatalf("calls = %v, want archive restarted", calls)
	}
	if m.mode != ssmModeSessions || m.selected != 0 || m.errorMsg != "" {
		t.Fatalf("mode = %v selected = %d error = %q, want archive selected", m.mode, m.selected, m.errorMsg)
	}
}

func TestRestartIgnoresRunningSession(t *testing.T) {
	m, service := newSessionsModel(t, "archive", "cache")

	m, cmd := update(t, m, key("r"))
	if m.mode != ssmModeSessions || cmd != nil || len(service.Calls()) != 0 {
		t.Fatalf("mode = %v calls = %v, a running session is not restarted", m.mode, service.Calls())
	}
}

func TestExpiredAWSSignInSignsInAgainKeepingTheTunnel(t *testing.T) {
	running := testSession(1, "archive")
	running.AWSSignInExpired = true
	service := newFakeService(running)
	m := newServiceModel(t, service)
	if view := plainView(m); !strings.Contains(view, "AWS sign-in expired") || !strings.Contains(view, "sign in to AWS") {
		t.Fatalf("the expired sign-in and its key are not shown:\n%s", view)
	}

	m, cmd := update(t, m, key("r"))
	if cmd == nil || m.mode != ssmModeSessions {
		t.Fatalf("mode = %v, want the sign-in sent while the session keeps running", m.mode)
	}
	cmd()
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "aws-signin 1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRestartRefusesTargetConnectedElsewhere(t *testing.T) {
	ended := testSession(1, "archive")
	ended.State = sessions.StateEnded
	m := newServiceModel(t, newFakeService(ended, testSession(2, "archive")))
	m = m.selectSession(0)

	m, cmd := update(t, m, key("r"))
	if m.mode != ssmModeSessions || cmd != nil || m.errorMsg != "archive is already connected in another session" {
		t.Fatalf("mode = %v error = %q, want the duplicate refused at once", m.mode, m.errorMsg)
	}
}

func TestOpenedSessionFreesItsRequestContext(t *testing.T) {
	m, service := newSessionsModel(t, "first")
	m, cmd := update(t, m, jumphostWizardDoneMsg{options: jumphost.Options{ResourceType: "rds", Name: "second"}})
	opened := openedMsg(t, cmd)
	if service.openCtx.Err() != nil {
		t.Fatal("the request context ended before the opening was answered")
	}
	update(t, m, opened)
	if service.openCtx.Err() == nil {
		t.Fatal("the request context of an answered opening is never canceled")
	}
}

func TestRestartFailureKeepsEndedSession(t *testing.T) {
	m, _ := newSessionsModel(t, "archive", "cache")
	m, _ = sendUpdate(t, m, 1, ended(""))
	m = m.selectSession(0)
	m, _ = update(t, m, key("r"))

	m, _ = update(t, m, ssmOpenedMsg{err: errors.New("archive is already connected in another session"), id: 1, prepareID: m.prepareID, restart: true})
	if m.mode != ssmModeSessions || len(m.sessions) != 2 || m.sessions[0].ID != 1 || m.errorMsg != "Could not restart: archive is already connected in another session" {
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
func escFromDashboardWizard(t *testing.T, discovery JumphostDiscovery) (ssmModel, tea.Cmd) {
	t.Helper()
	m := newSSMModel(t.Context(), newFakeService(testSession(1, "db")), discovery, jumphost.Options{}).subscribe()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = update(t, m, key("n"))
	m, _ = update(t, m, m.wizard.loadSavedCmd()())
	if !strings.Contains(m.render(), "esc") {
		t.Fatalf("the first wizard screen does not hint that esc goes back:\n%s", m.render())
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
	m := newServiceModel(t, newFakeService()).startupWizard()
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
	s := testSession(1, "db")
	for idx := range 200 {
		s.Lines = append(s.Lines, sessions.LogLine{Text: fmt.Sprintf("line %d", idx)})
	}
	m := newServiceModel(t, newFakeService(s))
	return m.selectSession(0)
}

func TestLogKeysJumpToTopAndResumeFollowing(t *testing.T) {
	for _, keys := range [][2]string{{"g", "G"}, {"home", "end"}} {
		m := newLongLogModel(t)

		m, _ = update(t, m, key(keys[0]))
		if m.viewport.YOffset() != 0 {
			t.Fatalf("%s: offset = %d, want the top of the log", keys[0], m.viewport.YOffset())
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
	m, service := newSessionsModel(t, "first")

	m, _ = update(t, m, key("x"))
	if view := plainView(m); !strings.Contains(view, "y  stop") || !strings.Contains(view, "n  cancel") {
		t.Fatalf("the dialog keys do not name y and n:\n%s", view)
	}
	m, cmd := update(t, m, key("n"))
	runServiceCmd(cmd)
	if m.mode != ssmModeSessions || len(service.Calls()) != 0 {
		t.Fatalf("mode = %v calls = %v, want n to keep the session", m.mode, service.Calls())
	}

	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("tab")) // focus Cancel: y still stops
	m, cmd = update(t, m, key("y"))
	runServiceCmd(cmd)
	if calls := service.Calls(); m.mode != ssmModeSessions || len(calls) != 1 || calls[0] != "stop 1" {
		t.Fatalf("mode = %v calls = %v, want y to stop the session", m.mode, calls)
	}
}

func TestQuitDialogShortcuts(t *testing.T) {
	m, service := newSessionsModel(t, "first")

	m, _ = update(t, m, key("q"))
	m, _ = update(t, m, key("n"))
	if m.mode != ssmModeSessions || m.quitting {
		t.Fatalf("mode = %v quitting = %v, want n to stay", m.mode, m.quitting)
	}
	m, _ = update(t, m, key("q"))
	m, cmd := update(t, m, key("y"))
	runServiceCmd(cmd)
	if calls := service.Calls(); !m.quitting || len(calls) != 1 || calls[0] != "stop-all" {
		t.Fatalf("quitting = %v calls = %v, want y to stop the sessions to quit", m.quitting, calls)
	}
	if !strings.Contains(plainView(m), "Stopping sessions...") {
		t.Fatalf("the dashboard does not say it waits for the sessions:\n%s", plainView(m))
	}
}

func TestCtrlCTwiceStopsAllAndQuits(t *testing.T) {
	m, service := newSessionsModel(t, "first", "second")

	m, _ = update(t, m, key("ctrl+c"))
	if m.mode != ssmModeConfirmQuit {
		t.Fatalf("mode = %v, want the quit dialog", m.mode)
	}
	m, cmd := update(t, m, key("ctrl+c"))
	runServiceCmd(cmd)
	if calls := service.Calls(); !m.quitting || len(calls) != 1 || calls[0] != "stop-all" {
		t.Fatalf("quitting = %v calls = %v, a second ctrl+c did not stop every session to quit", m.quitting, calls)
	}
	if _, cmd = update(t, m, key("ctrl+c")); !isQuit(cmd) {
		t.Fatal("a third ctrl+c did not quit without waiting")
	}
}

func TestCtrlCInStopDialogAsksToQuit(t *testing.T) {
	m, service := newSessionsModel(t, "first")

	m, _ = update(t, m, key("x"))
	m, cmd := update(t, m, key("ctrl+c"))
	runServiceCmd(cmd)
	if m.mode != ssmModeConfirmQuit || len(service.Calls()) != 0 {
		t.Fatalf("mode = %v calls = %v, want the quit dialog instead", m.mode, service.Calls())
	}
}

func TestStopKeyAsksTheServiceToStop(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	m := newServiceModel(t, service)

	m, _ = update(t, m, key("x"))
	_, cmd := update(t, m, key("enter"))
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "stop 1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestEventsUpdateTheSelectedSessionLog(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	m := newServiceModel(t, service)
	updated, _ := update(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventLines, Seq: 1, ID: 1,
		Lines: []sessions.LogLine{{Text: "Connection accepted for session"}}}})
	if view := updated.render(); !strings.Contains(view, "Connection accepted for session") {
		t.Fatalf("view misses the new line:\n%s", view)
	}
}

func TestStaleEventsAreIgnored(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	service.snapshot.Seq = 10
	m := newServiceModel(t, service)
	updated, _ := update(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventRemoved, Seq: 9, ID: 1}})
	if len(updated.sessions) != 1 {
		t.Fatal("an event older than the snapshot must be ignored")
	}
}

func TestResyncTakesANewSnapshot(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	m := newServiceModel(t, service)
	service.snapshot = sessions.Snapshot{Seq: 40, Sessions: []sessions.Session{testSession(1, "archive-db"), testSession(2, "cache")}}

	m, cmd := update(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventResync, Seq: 41}})
	m, _ = update(t, m, cmd())
	if len(m.sessions) != 2 || m.seq != 40 {
		t.Fatalf("sessions = %d seq = %d, want the new snapshot", len(m.sessions), m.seq)
	}
}

func TestNoticeEventIsShown(t *testing.T) {
	m, _ := newSessionsModel(t, "archive-db")
	m, _ = update(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventNotice, Seq: 1, Notice: "Could not remember the sign-in: disk full"}})
	if !strings.Contains(plainView(m), "Could not remember the sign-in: disk full") {
		t.Fatalf("the notice is not shown:\n%s", plainView(m))
	}
}

func TestSignInDialogOpensForTheSelectedSession(t *testing.T) {
	s := testSession(1, "archive-db")
	service := newFakeService(s)
	m := newServiceModel(t, service)
	s.SignIn = sessions.SignIn{State: sessions.SignInChoosing, Candidates: []jumphost.SignInCandidate{{Label: "a", User: "u1"}, {Label: "b", User: "u2"}}}
	m, _ = update(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventUpdated, Seq: 1, ID: 1, Session: &s}})
	if m.mode != ssmModeSignIn {
		t.Fatalf("mode = %v, want the sign-in dialog", m.mode)
	}
	_, cmd := update(t, m, key("2"))
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "choose 1 1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestOpeningSetsTheProjectDirectoryAndTheTerminal(t *testing.T) {
	service := newFakeService()
	m := newServiceModel(t, service)
	m.projectDir = "/work/app"
	m.interactive = func(context.Context, string, string, ...string) error { return nil }
	_, cmd := m.open(jumphost.Options{ResourceType: "rds", Name: "archive-db"}, "", "Preparing")
	runServiceCmd(cmd)
	if len(service.opened) != 1 || service.opened[0].Options.ProjectDir != "/work/app" || service.opened[0].Interactive == nil {
		t.Fatalf("opened = %+v", service.opened)
	}
}

func TestDynamoDBSessionShowsItsEndpointNotAPort(t *testing.T) {
	s := &sessions.Session{ResourceType: sessions.DynamoDBResourceType, Target: "eu-west-3", Profile: "dev", Region: "eu-west-3",
		Details: jumphost.SessionDetails{ConnectedServer: "dynamodb.eu-west-3.amazonaws.com"}}
	if got := s.Address(); got != "dynamodb.eu-west-3.amazonaws.com" {
		t.Fatalf("address = %q", got)
	}
	if got := sessionOrigin(s); got != "dev · eu-west-3" {
		t.Fatalf("origin = %q", got)
	}
}

func TestDashboardWizardKeepsWritesLocked(t *testing.T) {
	m := newDashboardModel(t.Context(), newFakeService(), fakeSessionRunner{}, 0, jumphost.Options{MCPAllowWrites: true}, &DashboardOptions{WritesLocked: true})
	if m.mode != ssmModeWizard || !m.wizard.writesLocked || !strings.Contains(m.wizard.mcpWritesToggle(), "imposed by the organization") {
		t.Fatalf("mode = %v locked = %v toggle = %q; want the wizard's writes locked", m.mode, m.wizard.writesLocked, m.wizard.mcpWritesToggle())
	}
}
