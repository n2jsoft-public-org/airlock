package ui

import (
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// sessionEnvironment is the environment of s under the environments setting of the dashboard.
func (m ssmModel) sessionEnvironment(s *sessions.Session) environment.Env {
	return sessions.EnvironmentOf(m.environments, s)
}

// isProduction reports whether s reaches production.
func (m ssmModel) isProduction(s *sessions.Session) bool {
	return m.sessionEnvironment(s).Production
}
