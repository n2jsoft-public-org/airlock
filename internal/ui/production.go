package ui

import (
	"path"
	"strings"

	"github.com/n2jsoft/airlock/internal/sessions"
)

// productionMatcher holds the patterns (* wildcard, any case) of the AWS profiles flagged as
// production, from the productionProfiles setting.
type productionMatcher []string

// matches reports whether profile is flagged as production.
func (p productionMatcher) matches(profile string) bool {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		return false
	}
	for _, pattern := range p {
		if ok, err := path.Match(strings.ToLower(pattern), profile); err == nil && ok {
			return true
		}
	}
	return false
}

// isProduction reports whether s reaches production: a direct database flagged so, or an AWS
// profile of the productionProfiles setting.
func (m ssmModel) isProduction(s *sessions.Session) bool {
	return s.Production || m.production.matches(s.Profile)
}
