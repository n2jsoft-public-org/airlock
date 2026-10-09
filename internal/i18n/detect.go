package i18n

import (
	"slices"
	"strings"
)

// Detect returns the language of the system: the first of LC_ALL, LC_MESSAGES and LANG that is
// set, else osLocale; English when it is not supported.
func Detect(getenv func(string) string, osLocale func() string) string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if lang := normalize(getenv(name)); lang != "" {
			return supported(lang)
		}
	}
	return supported(normalize(osLocale()))
}

// normalize keeps the language of a locale (fr_FR.UTF-8, fr-FR, fr_FR@rg=frzzzz → fr); C and
// POSIX name no language.
func normalize(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(locale, "_-.@"); i >= 0 {
		locale = locale[:i]
	}
	if locale == "c" || locale == "posix" {
		return ""
	}
	return locale
}

func supported(lang string) string {
	if slices.Contains(Supported, lang) {
		return lang
	}
	return "en"
}

// firstAppleLanguage reads the first entry of `defaults read -g AppleLanguages`: ("en-US", "fr-FR").
func firstAppleLanguage(out string) string {
	fields := strings.FieldsFunc(out, func(r rune) bool { return strings.ContainsRune("()\", \n\t", r) })
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
