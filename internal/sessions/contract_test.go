package sessions_test

import (
	"context"
	"testing"

	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

func TestManagerContract(t *testing.T) {
	sessionstest.RunContract(t, func(t *testing.T, runner *sessionstest.Runner, launcher *sessionstest.Launcher) sessions.Service {
		m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch})
		t.Cleanup(m.Shutdown)
		return m
	})
}
