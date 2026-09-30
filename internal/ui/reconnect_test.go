package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
)

// newReconnectModel returns two ready sessions, archive then cache, archive selected.
func newReconnectModel(t *testing.T) (m ssmModel, prepared *[]jumphost.Options) {
	t.Helper()
	m, prepared = newRestartModel(t)
	for _, s := range m.sessions {
		s.ready = true
	}
	return m.selectSession(0), prepared
}

// dropCache ends the cache session on its own, as an SSM idle timeout does.
func dropCache(t *testing.T, m ssmModel) ssmModel {
	t.Helper()
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[1].id, err: errors.New("session timed out")})
	return m
}

// reconnect fires the scheduled reconnection of s and returns the result of its preparation.
func reconnect(t *testing.T, m ssmModel, s *ssmSession) (ssmModel, ssmPreparedMsg) {
	t.Helper()
	m, cmd := update(t, m, ssmReconnectMsg{id: s.id})
	if cmd == nil {
		t.Fatal("the reconnection did not start")
	}
	msg, ok := cmd().(ssmPreparedMsg)
	if !ok {
		t.Fatal("the reconnection did not prepare the session")
	}
	return m, msg
}

func TestDroppedSessionReconnectsInTheBackground(t *testing.T) {
	m, prepared := newReconnectModel(t)
	m = dropCache(t, m)

	cache := m.sessions[1]
	if cache.reconnectAt.IsZero() || !strings.Contains(plainView(m), "Retry in 2s") {
		t.Fatalf("no reconnection scheduled:\n%s", plainView(m))
	}
	m, msg := reconnect(t, m, cache)
	if m.mode != ssmModeSessions || m.selected != 0 {
		t.Fatalf("mode = %v selected = %d, a reconnection must not take the screen", m.mode, m.selected)
	}
	want := jumphost.Options{ResourceType: "rds", Name: "cache", LocalPortNumber: 5433, AwsProfile: "dev", AwsRegion: "eu-west-3", DBUser: "app_ro", MCPAllowWrites: true, Unattended: true}
	if len(*prepared) != 1 || (*prepared)[0] != want {
		t.Fatalf("prepared = %+v, want %+v: the same session, never signing in", *prepared, want)
	}

	m, _ = update(t, m, msg)
	cache = m.sessions[1]
	if cache.state == ssmSessionEnded || cache.session.Target != "cache" || m.selected != 0 {
		t.Fatalf("state = %v selected = %d, want cache reconnected in its slot, archive still selected", cache.state, m.selected)
	}
	if cache.options.Unattended {
		t.Fatal("the reconnected session keeps Unattended: a manual restart would never sign in")
	}
	logs := m.renderLogLines(cache, 200)
	if !strings.Contains(logs, "session timed out") || !strings.Contains(logs, "Reconnected automatically (attempt 1/5)") {
		t.Fatalf("logs = %q, want the drop and the reconnection", logs)
	}
}

func TestSessionThatNeverWorkedIsNotReconnected(t *testing.T) {
	m, _ := newReconnectModel(t)
	m.sessions[1].ready = false
	m = dropCache(t, m)
	if !m.sessions[1].reconnectAt.IsZero() {
		t.Fatal("a session that never listened was scheduled for reconnection: its settings are likely wrong")
	}
}

func TestStoppedSessionIsNotReconnected(t *testing.T) {
	m, _ := newReconnectModel(t)
	m = m.selectSession(1)
	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("y"))
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[1].id})
	if !m.sessions[1].reconnectAt.IsZero() {
		t.Fatal("a session stopped with x was scheduled for reconnection")
	}
}

func TestClosingCancelsTheScheduledReconnection(t *testing.T) {
	m, prepared := newReconnectModel(t)
	m = dropCache(t, m)
	cache := m.sessions[1]

	m = m.selectSession(1)
	m, _ = update(t, m, key("x"))
	m, cmd := update(t, m, ssmReconnectMsg{id: cache.id})
	if cmd != nil || len(*prepared) != 0 || len(m.sessions) != 1 {
		t.Fatalf("prepared = %d sessions = %d, want a closed session left closed", len(*prepared), len(m.sessions))
	}
}

func TestClosingDuringTheReconnectionReleasesItsResult(t *testing.T) {
	m, _ := newReconnectModel(t)
	m = dropCache(t, m)
	m, msg := reconnect(t, m, m.sessions[1])

	m = m.selectSession(1)
	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, msg)
	if len(m.sessions) != 1 {
		t.Fatalf("sessions = %d, a closed session came back", len(m.sessions))
	}
}

func TestManualRestartTakesOverTheScheduledReconnection(t *testing.T) {
	m, prepared := newReconnectModel(t)
	m = dropCache(t, m)
	cache := m.sessions[1]

	m = m.selectSession(1)
	m, _ = update(t, m, key("r"))
	if m.mode != ssmModePreparing || !cache.reconnectAt.IsZero() {
		t.Fatalf("mode = %v, want r to restart now, replacing the scheduled reconnection", m.mode)
	}
	if _, cmd := update(t, m, ssmReconnectMsg{id: cache.id}); cmd != nil || len(*prepared) != 0 {
		t.Fatal("the scheduled reconnection still ran")
	}
}

func TestReconnectionsStopAfterTheLastAttempt(t *testing.T) {
	m, _ := newReconnectModel(t)
	m.sessions[1].reconnects = len(reconnectDelays)
	m = dropCache(t, m)
	if !m.sessions[1].reconnectAt.IsZero() || !strings.Contains(m.renderLogLines(m.sessions[1], 200), "press r") {
		t.Fatal("reconnections went on past the last attempt, or did not say how to retry")
	}
}

func TestSessionThatLastedResetsItsAttempts(t *testing.T) {
	m, _ := newReconnectModel(t)
	m.sessions[1].reconnects = len(reconnectDelays)
	m.sessions[1].startedAt = time.Now().Add(-2 * time.Minute)
	m = dropCache(t, m)
	if m.sessions[1].reconnectAt.IsZero() {
		t.Fatal("a session that ran for minutes did not get fresh attempts")
	}
}

// failReconnect drops cache, fires its reconnection and makes it fail with err.
func failReconnect(t *testing.T, err error) (m ssmModel, cache *ssmSession) {
	t.Helper()
	m, _ = newReconnectModel(t)
	m = dropCache(t, m)
	m, msg := reconnect(t, m, m.sessions[1])
	msg.session, msg.err = nil, err
	m, _ = update(t, m, msg)
	return m, m.sessions[1]
}

func TestFailedReconnectionIsRetried(t *testing.T) {
	_, cache := failReconnect(t, errors.New("could not connect to the endpoint URL"))
	if cache.reconnectAt.IsZero() || cache.reconnects != 1 {
		t.Fatal("a network failure, as after a laptop wakes up, was not retried")
	}
}

func TestReconnectionNeedingSignInStops(t *testing.T) {
	m, cache := failReconnect(t, fmt.Errorf("%w: the AWS credentials of profile dev are missing or expired", jumphost.ErrSignInRequired))
	logs := m.renderLogLines(cache, 200)
	if cache.reconnectPending() || !strings.Contains(logs, "press r") {
		t.Fatalf("an expired sign-in was retried, or did not say to press r:\n%s", logs)
	}
}
