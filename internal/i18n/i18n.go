// Package i18n translates the text airlock shows to people: English, or French. The language is
// set once at start (SetLanguage) and read by every renderer.
package i18n

import (
	"fmt"
	"sync/atomic"
)

// Supported lists the languages with a catalog; English is the fallback.
var Supported = []string{"en", "fr"}

var catalogs = map[string]map[string]string{"en": en, "fr": fr}

var current atomic.Value // string

// SetLanguage selects the catalog T reads; an unsupported language selects English.
func SetLanguage(lang string) {
	if _, ok := catalogs[lang]; !ok {
		lang = "en"
	}
	current.Store(lang)
}

// Language returns the selected language, English until SetLanguage is called.
func Language() string {
	if lang, ok := current.Load().(string); ok {
		return lang
	}
	return "en"
}

// T returns the text of key in the selected language, else in English, else the key itself.
func T(key string) string {
	if text, ok := catalogs[Language()][key]; ok {
		return text
	}
	if text, ok := catalogs["en"][key]; ok {
		return text
	}
	return key
}

// Format formats the text of key with args.
func Format(key string, args ...any) string {
	return fmt.Sprintf(T(key), LocalizeArgs(args)...)
}

// Plural formats n with key.one or key.other: English uses one for 1, French for 0 and 1.
func Plural(n int, key string) string {
	form := ".other"
	if n == 1 || (n == 0 && Language() == "fr") {
		form = ".one"
	}
	return Format(key+form, n)
}
