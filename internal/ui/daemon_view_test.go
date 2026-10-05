package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/sessions"
)

func newDaemonView(t *testing.T, service *fakeService) ssmModel {
	t.Helper()
	m := newServiceModel(t, service)
	m.detach = true
	return m
}

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func asModel(t *testing.T, model tea.Model) ssmModel {
	t.Helper()
	m, ok := model.(ssmModel)
	if !ok {
		t.Fatalf("model is %T", model)
	}
	return m
}

func TestQuitDetachesADaemonView(t *testing.T) {
	for _, key := range []tea.KeyMsg{keyRunes("q"), {Type: tea.KeyCtrlC}} {
		service := newFakeService(testSession(1, "archive-db"))
		m := newDaemonView(t, service)
		_, cmd := m.Update(key)
		if !isQuit(cmd) {
			t.Fatalf("%v must leave the view at once", key)
		}
		if calls := service.Calls(); len(calls) != 0 {
			t.Fatalf("detaching must not touch the sessions: %v", calls)
		}
	}
}

func TestStopAllAndQuitInADaemonView(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	m := newDaemonView(t, service)
	m = asModel(t, mustUpdate(t, m, keyRunes("Q")))
	if m.mode != ssmModeConfirmQuit || !strings.Contains(m.View(), "Stop every session and the daemon?") {
		t.Fatalf("mode = %v, view:\n%s", m.mode, m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "stop-all" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestStopAllReachesTheDaemonEvenWithoutActiveSessions(t *testing.T) {
	service := newFakeService()
	m := newDaemonView(t, service)
	m = asModel(t, mustUpdate(t, m, keyRunes("Q")))
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "stop-all" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestDaemonStoppedAfterStopAllQuits(t *testing.T) {
	m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
	m.quitting = true
	updated, cmd := m.Update(ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventClosed, Err: "daemon stopped"}})
	if !isQuit(cmd) || asModel(t, updated).exitNotice != "The airlock daemon stopped." {
		t.Fatal("the view must quit once the daemon it asked to stop is gone")
	}
}

func TestCtrlCOnTheDaemonQuitDialogOnlyLeaves(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	m := newDaemonView(t, service)
	m = asModel(t, mustUpdate(t, m, keyRunes("Q")))
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuit(cmd) || len(service.Calls()) != 0 {
		t.Fatalf("ctrl+c must leave without stopping: calls = %v", service.Calls())
	}
}

func TestDaemonHelpListsDetachAndStopAll(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	var text []string
	for _, group := range m.helpGroups() {
		for _, k := range group.keys {
			text = append(text, k[0]+" "+k[1])
		}
	}
	joined := strings.Join(text, "\n")
	for _, want := range []string{"Leave this view (sessions keep running)", "Stop every session and the daemon"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("help misses %q:\n%s", want, joined)
		}
	}
}

func TestLostConnectionShowsABannerAndReconnects(t *testing.T) {
	service := newFakeService(testSession(1, "archive-db"))
	m := newDaemonView(t, service)
	replacement := newFakeService(testSession(2, "orders-db"))
	m.reconnect = func(context.Context) (sessions.Service, error) { return replacement, nil }

	m = asModel(t, mustUpdate(t, m, ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventClosed, Err: "connection to the daemon lost"}}))
	if !strings.Contains(m.View(), "connection to the daemon lost") {
		t.Fatalf("view misses the banner:\n%s", m.View())
	}
	updated, cmd := m.Update(keyRunes("r"))
	m = asModel(t, updated)
	updated, _ = m.Update(cmd())
	m = asModel(t, updated)
	if m.connLost != "" || len(m.sessions) != 1 || m.sessions[0].Target != "orders-db" {
		t.Fatalf("after reconnect: lost = %q, sessions = %+v", m.connLost, m.sessions)
	}
}

func TestFailedReconnectKeepsTheBanner(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	m.connLost = "connection to the daemon lost"
	m.reconnect = func(context.Context) (sessions.Service, error) { return nil, errors.New("version mismatch") }
	updated, cmd := m.Update(keyRunes("r"))
	m = asModel(t, updated)
	m = asModel(t, mustUpdate(t, m, cmd()))
	if !strings.Contains(m.connLost, "could not reach the daemon: version mismatch") {
		t.Fatalf("connLost = %q", m.connLost)
	}
}

func TestClosedChannelOfADaemonViewIsALostConnection(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	m = asModel(t, mustUpdate(t, m, ssmEventMsg{ok: false}))
	if m.connLost != "connection to the daemon lost" {
		t.Fatalf("connLost = %q", m.connLost)
	}
}

func mustUpdate(t *testing.T, m ssmModel, msg tea.Msg) tea.Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next
}

func TestDaemonStoppedOnPurposeQuitsAnyView(t *testing.T) {
	m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
	updated, cmd := m.Update(ssmEventMsg{ok: true, event: sessions.Event{Kind: sessions.EventClosed, Err: "daemon stopped"}})
	if !isQuit(cmd) || asModel(t, updated).connLost != "" {
		t.Fatal("a daemon stopped on purpose ends the view without a banner")
	}
}

func TestReconnectRunsOnceAtATime(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	m.connLost = "connection to the daemon lost"
	calls := 0
	m.reconnect = func(context.Context) (sessions.Service, error) { calls++; return newFakeService(), nil }
	updated, cmd := m.Update(keyRunes("r"))
	m = asModel(t, updated)
	_, second := m.Update(keyRunes("r"))
	if second != nil {
		t.Fatal("a second r must not start another reconnection")
	}
	cmd()
	if calls != 1 {
		t.Fatalf("reconnect calls = %d", calls)
	}
}

func TestStaleSubscriptionMessagesAreIgnoredAfterReconnect(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	old := make(chan sessions.Event)
	m.events = make(chan sessions.Event)
	updated, _ := m.Update(ssmEventMsg{events: old, ok: false})
	updated2, _ := updated.Update(ssmEventMsg{events: old, ok: true, event: sessions.Event{Kind: sessions.EventClosed, Err: "x"}})
	if asModel(t, updated2).connLost != "" {
		t.Fatal("a message of an old subscription must be ignored")
	}
}

func TestLostConnectionLeavesDialogsAndShowsOnlyBannerHints(t *testing.T) {
	m := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
	m.mode = ssmModeConfirmStop
	m = asModel(t, mustUpdate(t, m, ssmEventMsg{ok: false}))
	if m.mode != ssmModeSessions || strings.Contains(m.View(), "new") && strings.Contains(m.View(), "switch") {
		t.Fatalf("mode = %v, view:\n%s", m.mode, m.View())
	}
}

func TestRefusedStopAllLeavesTheViewUsable(t *testing.T) {
	m := newDaemonView(t, newFakeService())
	m.quitting = true
	m = asModel(t, mustUpdate(t, m, ssmActionMsg{err: errors.New("boom"), prefix: "Could not stop the sessions: "}))
	if m.quitting {
		t.Fatal("quitting must be cleared")
	}
	_, cmd := m.Update(keyRunes("q"))
	if !isQuit(cmd) {
		t.Fatal("q must leave")
	}
}
