package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

func TestDaemonViewStartupOpensTheWizardOnlyWhenTheCommandNamedSomething(t *testing.T) {
	cases := []struct {
		name, openDirect, resourceType string
		wizard                         bool
	}{
		{name: "airlock direct <name>", openDirect: "billing", wizard: true},
		{name: "airlock <type>", resourceType: "rds", wizard: true},
		{name: "airlock", wizard: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := newFakeService(testSession(1, "archive-db"))
			m := newSSMModel(t.Context(), service, fakeSessionRunner{}, jumphost.Options{ResourceType: tc.resourceType})
			m.detach, m.openDirect = true, tc.openDirect
			m = m.subscribe().startupView(0)
			if got := m.mode == ssmModeWizard; got != tc.wizard {
				t.Fatalf("wizard = %v, want %v (mode %v)", got, tc.wizard, m.mode)
			}
			if m.wizard.openDirect != tc.openDirect {
				t.Fatalf("wizard opens on %q, want %q", m.wizard.openDirect, tc.openDirect)
			}
		})
	}
}

func TestDaemonViewStartupSelectsTheSessionItOpened(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"), testSession(2, "orders-db"))
	m := newSSMModel(t.Context(), service, fakeSessionRunner{}, jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	m.detach = true
	m = m.subscribe().startupView(1)
	if m.mode != ssmModeSessions || m.selected != 0 {
		t.Fatalf("mode = %v, selected = %d", m.mode, m.selected)
	}
}

func TestResyncAfterStopAllQuitsOnceNothingRuns(t *testing.T) {
	m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
	m.quitting = true
	stopped := testSession(1, "archive-db")
	stopped.State = sessions.StateEnded
	_, cmd := m.Update(ssmSnapshotMsg{snapshot: sessions.Snapshot{Sessions: []sessions.Session{stopped}, Seq: 9}})
	if !isQuit(cmd) {
		t.Fatal("a resync showing every session ended must end \"stop all & quit\"")
	}
}

func TestResyncSelectsThePendingSessionAndOpensItsSignIn(t *testing.T) {
	m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
	m.pendingSelect = 2
	opened := testSession(2, "orders-db")
	opened.HasSignIn = true
	opened.SignIn = sessions.SignIn{State: sessions.SignInChoosing, Candidates: []jumphost.SignInCandidate{{Label: "a", User: "u"}}}
	m = asModel(t, mustUpdate(t, m, ssmSnapshotMsg{snapshot: sessions.Snapshot{
		Sessions: []sessions.Session{testSession(1, "archive-db"), opened}, Seq: 9,
	}}))
	if m.pendingSelect != 0 || m.selected != 1 || m.mode != ssmModeSignIn {
		t.Fatalf("pending = %d, selected = %d, mode = %v", m.pendingSelect, m.selected, m.mode)
	}
}

func TestReconnectForgetsTheIDsOfTheLostDaemon(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	m.connLost, m.discarded, m.pendingSelect = "connection to the daemon lost", []sessions.ID{1}, 1
	// The restarted daemon numbers from 1 again: its session 1 is another one.
	replacement := newFakeService(testSession(1, "orders-db"))
	m = asModel(t, mustUpdate(t, m, ssmReconnectedMsg{service: replacement}))
	if len(m.discarded) != 0 || m.pendingSelect != 0 {
		t.Fatalf("discarded = %v, pending = %d", m.discarded, m.pendingSelect)
	}
	m, cmd := sendUpdate(t, m, 1, ended(""))
	runServiceCmd(cmd)
	if calls := replacement.Calls(); slices.Contains(calls, "remove 1") {
		t.Fatalf("an unrelated session was closed: %v", calls)
	}
}

func TestFailedFirstSubscribeOfADaemonViewIsALostConnection(t *testing.T) {
	service := newFakeService()
	service.subscribeErr = errors.New("broken pipe")
	m := newSSMModel(t.Context(), service, fakeSessionRunner{}, jumphost.Options{})
	m.detach = true
	m.reconnect = func(context.Context) (sessions.Service, error) {
		return newFakeService(testSession(1, "archive-db")), nil
	}
	m = m.subscribe().startupView(0)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.connLost == "" || m.mode != ssmModeSessions || !strings.Contains(m.View(), "broken pipe") {
		t.Fatalf("connLost = %q, mode = %v, view:\n%s", m.connLost, m.mode, m.View())
	}
	updated, cmd := m.Update(keyRunes("r"))
	if cmd == nil {
		t.Fatal("r must reconnect")
	}
	m = asModel(t, mustUpdate(t, asModel(t, updated), cmd()))
	if m.connLost != "" || len(m.sessions) != 1 {
		t.Fatalf("after reconnect: lost = %q, sessions = %d", m.connLost, len(m.sessions))
	}
}

// choosingModel is a daemon view on the sign-in dialog of session 1, over service.
func choosingModel(t *testing.T, service *fakeService) ssmModel {
	t.Helper()
	s := testSession(1, "archive-db")
	s.HasSignIn = true
	s.SignIn = sessions.SignIn{State: sessions.SignInChoosing, Candidates: []jumphost.SignInCandidate{{Label: "a", User: "u1"}, {Label: "b", User: "u2"}}}
	service.snapshot = sessions.Snapshot{Sessions: []sessions.Session{s}}
	m := newDaemonView(t, service)
	m.mode = ssmModeSignIn
	return m
}

func TestChoiceRefusedBecauseAnotherViewAnsweredIsIgnored(t *testing.T) {
	service := newFakeService()
	m := choosingModel(t, service)
	service.chooseErr = errors.New("no credentials to choose for archive-db")
	_, cmd := m.Update(keyRunes("1"))
	// The other view's choice won: the daemon is checking it.
	service.snapshot.Sessions[0].SignIn.State = sessions.SignInChecking
	m = asModel(t, mustUpdate(t, m, cmd()))
	if m.errorMsg != "" {
		t.Fatalf("errorMsg = %q", m.errorMsg)
	}
}

func TestChoiceRefusedWhileStillChoosingIsShown(t *testing.T) {
	service := newFakeService()
	m := choosingModel(t, service)
	service.chooseErr = errors.New("no credentials to choose for archive-db")
	_, cmd := m.Update(keyRunes("1"))
	m = asModel(t, mustUpdate(t, m, cmd()))
	if !strings.Contains(m.errorMsg, "no credentials to choose") {
		t.Fatalf("errorMsg = %q", m.errorMsg)
	}
}
