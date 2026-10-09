package sessions

import "github.com/n2jsoft/airlock/internal/i18n"

// errTexts is err for agents (English) and for the dashboards (the user's language; empty when it
// is the English one).
func errTexts(err error) (english, local string) {
	if err == nil {
		return "", ""
	}
	english = err.Error()
	if local = i18n.Localize(err); local == english {
		local = ""
	}
	return english, local
}
