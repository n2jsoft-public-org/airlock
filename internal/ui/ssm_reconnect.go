package ui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// reconnectDelays are the waits before each automatic reconnection of a dropped session, growing
// so a network still down after a laptop wakes up gets time to come back; their count caps the
// attempts in a row.
var reconnectDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}

// reconnectResetAfter is how long a session must have run for its next drop to get fresh attempts.
const reconnectResetAfter = time.Minute

// ssmReconnectMsg fires the scheduled reconnection of session id.
type ssmReconnectMsg struct {
	id int
}

// reconnectPending reports that s is waiting for, or going through, an automatic reconnection.
func (s *ssmSession) reconnectPending() bool {
	return s.reconnecting || !s.reconnectAt.IsZero()
}

// scheduleReconnect plans the automatic reconnection of s, which just ended. Only a session that
// ended on its own after it worked (SSM idle timeout, network drop, laptop sleep), or that a
// reconnection opened, is reconnected: one stopped by the user, or that never listened (its
// settings are likely wrong), is left for r.
func (m ssmModel) scheduleReconnect(s *ssmSession) tea.Cmd {
	// A direct session has no tunnel that drops: it only ends when stopped.
	if s.direct() || s.stopRequested || m.quitting || (!s.ready && s.reconnects == 0) {
		return nil
	}
	if time.Since(s.startedAt) >= reconnectResetAfter {
		s.reconnects = 0
	}
	return m.retryReconnect(s)
}

// retryReconnect schedules the next attempt of s, or gives up after the last one.
func (m ssmModel) retryReconnect(s *ssmSession) tea.Cmd {
	if s.reconnects >= len(reconnectDelays) {
		s.appendLines(fmt.Sprintf("Automatic reconnection gave up after %d attempts: press r to reconnect.", len(reconnectDelays)))
		return nil
	}
	delay := reconnectDelays[s.reconnects]
	s.reconnectAt = time.Now().Add(delay)
	s.appendLines(fmt.Sprintf("Reconnecting automatically in %s (attempt %d/%d): r reconnects now, x closes the session.",
		delay, s.reconnects+1, len(reconnectDelays)))
	id := s.id
	return tea.Tick(delay, func(time.Time) tea.Msg { return ssmReconnectMsg{id: id} })
}

// handleReconnect prepares the scheduled reconnection of a session in the background, never
// signing in: nobody may be there to answer.
func (m ssmModel) handleReconnect(msg ssmReconnectMsg) (tea.Model, tea.Cmd) {
	s := m.session(msg.id)
	// Closed, restarted by hand or quitting meanwhile: nothing to reconnect.
	if s == nil || s.active() || s.reconnectAt.IsZero() || m.quitting {
		return m, nil
	}
	s.reconnectAt = time.Time{}
	if m.activeSessionIndex(s.key()) >= 0 {
		s.appendLines(s.session.Target + " is connected in another session: no automatic reconnection.")
		m.refreshSelected(s)
		return m, nil
	}

	s.reconnecting = true
	pending := ssmPreparedMsg{options: restartOptions(s), replaces: s.id, background: true}
	pending.options.Unattended = true
	// Canceled when the session is closed or restarted by hand meanwhile.
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancelReconnect = cancel
	return m, func() tea.Msg {
		result := pending
		result.session, result.err = m.runner.Prepare(ctx, pending.options)
		return result
	}
}

// handleReconnected opens the reconnected session in the slot of the dropped one, or retries.
func (m ssmModel) handleReconnected(msg ssmPreparedMsg) (tea.Model, tea.Cmd) {
	index := m.sessionIndex(msg.replaces)
	// Closed or restarted by hand meanwhile: free what the reconnection reserved.
	if index < 0 || !m.sessions[index].reconnecting || m.quitting {
		if msg.session != nil {
			msg.session.Release()
		}
		return m, nil
	}

	dropped := m.sessions[index]
	dropped.reconnecting, dropped.cancelReconnect = false, nil
	if msg.err != nil {
		dropped.appendLines("Automatic reconnection failed: " + msg.err.Error())
		var cmd tea.Cmd
		if errors.Is(msg.err, jumphost.ErrSignInRequired) {
			dropped.appendLines("Signing in again needs you: press r to reconnect.")
		} else {
			dropped.reconnects++
			cmd = m.retryReconnect(dropped)
		}
		m.refreshSelected(dropped)
		return m, cmd
	}

	options := msg.options
	// A later restart by hand may sign in again.
	options.Unattended = false
	note := fmt.Sprintf("Reconnected automatically (attempt %d/%d).", dropped.reconnects+1, len(reconnectDelays))
	s := m.newSession(msg.session, options, dropped.lines, note)
	s.reconnects = dropped.reconnects + 1
	m.sessions[index] = s
	if m.selected == index {
		m.refreshViewport()
		m.viewport.GotoBottom()
	}
	cmd := m.startSession(s)
	return m, cmd
}

// reconnectStatus is the status label of an ended session waiting for its reconnection, short
// enough for a sidebar card next to its local address.
func reconnectStatus(s *ssmSession) string {
	if s.reconnecting {
		return "Reconnecting"
	}
	return fmt.Sprintf("Retry in %ds", int(math.Ceil(time.Until(s.reconnectAt).Seconds())))
}
