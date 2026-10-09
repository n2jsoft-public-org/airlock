package jumphost

import (
	"testing"

	"github.com/n2jsoft/airlock/internal/i18n"
)

func TestJumphostErrorsLocalize(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	err := NewSessionRegistry().CheckPort(0)
	if err == nil || err.Error() != "port 0 is out of range (1-65535)" {
		t.Fatalf("Error() = %v, want English", err)
	}
	if got := i18n.Localize(err); got != "le port 0 est hors limites (1-65535)" {
		t.Fatalf("Localize = %q, want French", got)
	}
}

func TestInteractiveErrorLocalizes(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	err := RunInteractive(t.Context(), "sign in", "aws", "sso", "login")
	if err == nil || err.Error() != "no terminal to run aws" {
		t.Fatalf("Error() = %v, want English", err)
	}
	if got := i18n.Localize(err); got != "aucun terminal pour lancer aws" {
		t.Fatalf("Localize = %q, want French", got)
	}
}
