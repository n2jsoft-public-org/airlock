package i18n

import "golang.org/x/sys/windows"

// OSLocale returns the first Windows UI language of the user, e.g. fr-FR.
func OSLocale() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langs[0]
}
