package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

// ReconnectDelays are the waits before each automatic reconnection of a dropped session, growing
// so a network still down after a laptop wakes up gets time to come back; their count caps the
// attempts in a row.
var ReconnectDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}

// reconnectResetAfter is how long a session must have run for its next drop to get fresh attempts.
const reconnectResetAfter = time.Minute

// scheduleReconnectLocked plans the automatic reconnection of e, which just ended. Only a session
// that ended on its own after it worked, or that a reconnection opened, is reconnected.
func (m *Manager) scheduleReconnectLocked(e *entry) {
	if e.info.NoTunnel() || e.info.StopRequested || m.closing || (!e.info.Ready && e.info.Reconnects == 0) {
		return
	}
	if m.now().Sub(e.info.StartedAt) >= reconnectResetAfter {
		e.info.Reconnects = 0
	}
	m.retryReconnectLocked(e)
}

func (m *Manager) retryReconnectLocked(e *entry) {
	if e.info.Reconnects >= len(ReconnectDelays) {
		m.lines(e, fmt.Sprintf("Automatic reconnection gave up after %d attempts: press r to reconnect.", len(ReconnectDelays)))
		return
	}
	delay := ReconnectDelays[e.info.Reconnects]
	e.info.ReconnectAt = m.now().Add(delay)
	m.lines(e, fmt.Sprintf("Reconnecting automatically in %s (attempt %d/%d): r reconnects now, x closes the session.",
		delay, e.info.Reconnects+1, len(ReconnectDelays)))
	m.changed(e)
	e.stopTimer = m.afterFunc(delay, func() { m.reconnect(e) })
}

// reconnect prepares the scheduled reconnection of e, never signing in: nobody may be there.
func (m *Manager) reconnect(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentLocked(e) || e.info.Active() || e.info.ReconnectAt.IsZero() || m.closing {
		return
	}
	e.info.ReconnectAt, e.stopTimer = time.Time{}, nil
	if m.activeWithKeyLocked(e.info.Key(), e) {
		m.lines(e, e.info.Target+" is connected in another session: no automatic reconnection.")
		m.changed(e)
		return
	}
	e.info.Reconnecting = true
	m.changed(e)
	options := restartOptions(e)
	options.Unattended = true
	ctx, cancel := context.WithCancel(m.ctx)
	e.cancelReconnect = cancel
	go func() {
		session, sessionCancel, identity, err := m.prepare(ctx, &options, nil)
		m.reconnected(e, &options, session, sessionCancel, identity, err)
	}()
}

func (m *Manager) reconnected(e *entry, options *jumphost.Options, session *jumphost.Session, cancel context.CancelFunc, identity IdentityChecker, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentLocked(e) || !e.info.Reconnecting || m.closing {
		if session != nil {
			session.Release()
			cancel()
		}
		return
	}
	e.info.Reconnecting, e.cancelReconnect = false, nil
	if err != nil {
		m.lines(e, "Automatic reconnection failed: "+err.Error())
		if errors.Is(err, jumphost.ErrSignInRequired) {
			m.lines(e, "Signing in again needs you: press r to reconnect.")
		} else {
			e.info.Reconnects++
			m.retryReconnectLocked(e)
		}
		m.changed(e)
		return
	}
	// A later restart by hand may sign in again.
	options.Unattended = false
	note := fmt.Sprintf("Reconnected automatically (attempt %d/%d).", e.info.Reconnects+1, len(ReconnectDelays))
	m.startLocked(m.replaceLocked(e, session, options, cancel, identity, note, e.info.Reconnects+1))
}

// Restart implements Service.
func (m *Manager) Restart(ctx context.Context, id ID, interactive jumphost.InteractiveFunc) error {
	m.mu.Lock()
	e := m.findLocked(id)
	switch {
	case e == nil:
		m.mu.Unlock()
		return ErrUnknownSession
	case e.info.Active():
		m.mu.Unlock()
		return i18n.NewError("sessions.reconnect.still_running", e.info.Target)
	case m.activeWithKeyLocked(e.info.Key(), e):
		m.mu.Unlock()
		return i18n.NewError("sessions.reconnect.already_connected", e.info.Target)
	case m.closing:
		m.mu.Unlock()
		return ErrClosed
	}
	// A restart by hand takes over an automatic reconnection, whose result is then released.
	m.stopReconnectingLocked(e)
	m.changed(e)
	options := restartOptions(e)
	m.mu.Unlock()

	session, cancel, identity, err := m.prepare(ctx, &options, interactive)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentLocked(e) || m.closing {
		session.Release()
		cancel()
		return ErrUnknownSession
	}
	m.startLocked(m.replaceLocked(e, session, &options, cancel, identity, "Restarting session...", 0))
	return nil
}

// replaceLocked puts the session prepared again for old in its place, keeping its ID and log;
// reconnects counts the automatic reconnections in a row that led to it.
func (m *Manager) replaceLocked(old *entry, session *jumphost.Session, options *jumphost.Options, cancel context.CancelFunc, identity IdentityChecker, note string, reconnects int) *entry {
	e := m.newEntryLocked(session, options, cancel, identity, old.info.ID, old.info.Lines, note)
	e.info.Reconnects = reconnects
	m.entries[m.indexLocked(old)] = e
	m.publish(&Event{Kind: EventReplaced, ID: e.info.ID, Session: new(e.info.Clone())})
	return e
}

// activeWithKeyLocked reports whether a running session other than except has key.
func (m *Manager) activeWithKeyLocked(key string, except *entry) bool {
	for _, other := range m.entries {
		if other != except && other.info.Active() && other.info.Key() == key {
			return true
		}
	}
	return false
}

func (m *Manager) stopReconnectingLocked(e *entry) {
	if e.stopTimer != nil {
		e.stopTimer()
		e.stopTimer = nil
	}
	if e.cancelReconnect != nil {
		e.cancelReconnect()
		e.cancelReconnect = nil
	}
	e.info.ReconnectAt, e.info.Reconnecting = time.Time{}, false
}

// restartOptions prepares the session of e again: same target, profile, region, local port and
// sign-in choices, so the tools pointed at it reconnect as they were.
func restartOptions(e *entry) jumphost.Options {
	options := e.options
	options.ResourceType = e.session.ResourceType
	options.Name = e.session.Target
	options.AwsProfile = e.session.Profile
	options.AwsRegion = e.session.Region
	options.LocalPortNumber = e.session.LocalPort
	return options
}
