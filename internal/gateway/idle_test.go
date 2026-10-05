package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

func TestSweepClosesIdleAgentSessions(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	idle := openDirect(t, m, runner, "idle", nil, false, "claude-code")
	used := openDirect(t, m, runner, "used", nil, false, "claude-code")
	mine := openDirect(t, m, runner, "mine", nil, false, "")
	settings := func() Settings { return Settings{IdleTimeout: 30 * time.Minute} }
	r := newRelay(t.Context(), m, time.Now, settings)
	tools := &toolset{manager: m, relay: r, settings: settings}
	snapshot, err := m.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	start := snapshot.Sessions[0].StartedAt
	r.mu.Lock()
	r.lastCall[used] = start.Add(20 * time.Minute)
	r.mu.Unlock()

	tools.sweep(t.Context(), start.Add(31*time.Minute))

	snapshot, err = m.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snapshot.Sessions {
		switch s.ID {
		case idle:
			if s.Active() {
				t.Fatal("an agent session idle for 31m must close")
			}
			if !logged(&s, "closed after 30m without agent activity") {
				t.Fatalf("lines = %+v", s.Lines)
			}
		case used, mine:
			if !s.Active() {
				t.Fatalf("session %d must keep running", s.ID)
			}
		}
	}
}

func logged(s *sessions.Session, text string) bool {
	for _, line := range s.Lines {
		if strings.Contains(line.Text, text) {
			return true
		}
	}
	return false
}
