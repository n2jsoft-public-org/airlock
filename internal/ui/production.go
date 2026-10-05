package ui

import "github.com/n2jsoft/airlock/internal/sessions"

// productionMatcher holds the patterns of the productionProfiles setting (see
// sessions.ProductionProfiles).
type productionMatcher []string

// matches reports whether profile is flagged as production.
func (p productionMatcher) matches(profile string) bool {
	return sessions.ProductionProfiles(p).Matches(profile)
}

// isProduction reports whether s reaches production: a direct database flagged so, or an AWS
// profile of the productionProfiles setting.
func (m ssmModel) isProduction(s *sessions.Session) bool {
	return sessions.ProductionProfiles(m.production).IsProduction(s)
}
