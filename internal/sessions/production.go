package sessions

import (
	"path"
	"strings"
)

// ProductionProfiles holds the patterns (* wildcard, any case) of the AWS profiles flagged as
// production, from the productionProfiles setting.
type ProductionProfiles []string

// Matches reports whether profile is flagged as production.
func (p ProductionProfiles) Matches(profile string) bool {
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

// IsProduction reports whether s reaches production: a direct database flagged so, or an AWS
// profile of p.
func (p ProductionProfiles) IsProduction(s *Session) bool {
	return s.Production || p.Matches(s.Profile)
}
