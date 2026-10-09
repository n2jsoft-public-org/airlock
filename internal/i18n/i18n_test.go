package i18n

import (
	"testing"
)

// useCatalogs swaps the catalogs for the test and restores them, with the language.
func useCatalogs(t *testing.T, enEntries, frEntries map[string]string) {
	t.Helper()
	saved, savedLang := catalogs, Language()
	catalogs = map[string]map[string]string{"en": enEntries, "fr": frEntries}
	t.Cleanup(func() {
		catalogs = saved
		SetLanguage(savedLang)
	})
}

func TestTFallsBackToEnglishThenToTheKey(t *testing.T) {
	useCatalogs(t, map[string]string{"a": "A", "b": "B"}, map[string]string{"a": "Á"})
	SetLanguage("fr")
	if got := T("a"); got != "Á" {
		t.Errorf(`T("a") = %q, want Á`, got)
	}
	if got := T("b"); got != "B" {
		t.Errorf(`T("b") = %q, want the English B`, got)
	}
	if got := T("missing"); got != "missing" {
		t.Errorf(`T("missing") = %q, want the key`, got)
	}
}

func TestSetLanguageRefusesUnsupported(t *testing.T) {
	useCatalogs(t, map[string]string{}, map[string]string{})
	SetLanguage("de")
	if got := Language(); got != "en" {
		t.Fatalf("Language() = %q after SetLanguage(de), want en", got)
	}
}

func TestTfFormats(t *testing.T) {
	useCatalogs(t, map[string]string{"s": "stops %s"}, map[string]string{"s": "arrête %s"})
	SetLanguage("fr")
	if got := Format("s", "db"); got != "arrête db" {
		t.Errorf("Format = %q, want arrête db", got)
	}
}

func TestPluralRules(t *testing.T) {
	entries := map[string]string{"c.one": "%d one", "c.other": "%d other"}
	useCatalogs(t, entries, entries)
	for _, tc := range []struct {
		lang string
		want string
		n    int
	}{
		{"en", "0 other", 0}, {"en", "1 one", 1}, {"en", "2 other", 2},
		{"fr", "0 one", 0}, {"fr", "1 one", 1}, {"fr", "2 other", 2},
	} {
		SetLanguage(tc.lang)
		if got := Plural(tc.n, "c"); got != tc.want {
			t.Errorf("%s Plural(%d) = %q, want %q", tc.lang, tc.n, got, tc.want)
		}
	}
}

func TestDetect(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		os   string
		want string
	}{
		{"LANG french", map[string]string{"LANG": "fr_FR.UTF-8"}, "", "fr"},
		{"LC_ALL wins over LANG", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "fr_FR.UTF-8"}, "", "en"},
		{"LC_MESSAGES wins over LANG", map[string]string{"LC_MESSAGES": "fr_CA", "LANG": "en_US"}, "", "fr"},
		{"Canadian french", map[string]string{"LANG": "fr_CA.UTF-8"}, "", "fr"},
		{"unsupported stops the search", map[string]string{"LC_ALL": "de_DE.UTF-8", "LANG": "fr_FR"}, "fr-FR", "en"},
		{"C.UTF-8 counts as unset", map[string]string{"LANG": "C.UTF-8"}, "fr-FR", "fr"},
		{"POSIX counts as unset", map[string]string{"LC_ALL": "POSIX"}, "fr-FR", "fr"},
		{"OS language with region", map[string]string{}, "fr-FR", "fr"},
		{"OS locale with modifier", map[string]string{}, "fr_FR@rg=frzzzz", "fr"},
		{"nothing known", map[string]string{}, "", "en"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(name string) string { return tc.env[name] }
			if got := Detect(getenv, func() string { return tc.os }); got != tc.want {
				t.Fatalf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseAppleLanguages(t *testing.T) {
	out := "(\n    \"en-US\",\n    \"fr-FR\"\n)\n"
	if got := firstAppleLanguage(out); got != "en-US" {
		t.Fatalf("firstAppleLanguage = %q, want en-US", got)
	}
	if got := firstAppleLanguage(""); got != "" {
		t.Fatalf("firstAppleLanguage(empty) = %q, want empty", got)
	}
}
