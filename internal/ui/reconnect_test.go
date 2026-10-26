package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

func TestReconnectStatusCountsDownToTheNextAttempt(t *testing.T) {
	s := testSession(1, "cache")
	s.State, s.ReconnectAt = sessions.StateEnded, time.Now().Add(2*time.Second)
	m := newServiceModel(t, newFakeService(s))
	if view := plainView(m); !strings.Contains(view, "Retry in 2s") || !strings.Contains(view, "pending") {
		t.Fatalf("the scheduled reconnection is not shown:\n%s", view)
	}

	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) { s.ReconnectAt, s.Reconnecting = time.Time{}, true })
	if view := plainView(m); !strings.Contains(view, "Reconnecting") {
		t.Fatalf("the reconnection in progress is not shown:\n%s", view)
	}
}
