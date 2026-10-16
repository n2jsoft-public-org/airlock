package config

import (
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/i18n"
)

func TestLanguageDefaultsToAuto(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != LanguageAuto {
		t.Fatalf("Language = %q, want auto", cfg.Language)
	}
}

func TestSetLanguageValidates(t *testing.T) {
	path := writeConfig(t, `{}`)
	if err := Set(path, nil, "language", " FR "); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "fr" {
		t.Fatalf("Language = %q, want fr", cfg.Language)
	}
	if err := Set(path, nil, "language", "de"); err == nil || !strings.Contains(err.Error(), "auto, en, fr") {
		t.Fatalf("Set(language, de) = %v, want an error listing auto, en, fr", err)
	}
}

func TestEveryKeyHasADescriptionInEveryLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("en") })
	for _, lang := range i18n.Supported {
		i18n.SetLanguage(lang)
		for _, key := range append(Keys, deprecatedProductionProfiles) {
			if d := key.Description(); d == "" || d == "config.key."+key.Name {
				t.Errorf("%s: %s has no description", lang, key.Name)
			}
		}
	}
}

func TestOnlyLanguageAndOrganizationConfigArePersonal(t *testing.T) {
	for _, key := range Keys {
		if key.Personal != (key.Name == "language" || key.Name == "organizationConfig") {
			t.Errorf("%s: Personal = %v", key.Name, key.Personal)
		}
	}
}

func TestConfigErrorsLocalize(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("en") })
	err := Set(writeConfig(t, `{}`), nil, "language", "de")
	i18n.SetLanguage("en")
	if !strings.Contains(err.Error(), "unknown language") {
		t.Fatalf("Error() = %q, want English", err.Error())
	}
	i18n.SetLanguage("fr")
	if got := i18n.Localize(err); !strings.Contains(got, "langue inconnue") {
		t.Fatalf("Localize = %q, want French", got)
	}
}
