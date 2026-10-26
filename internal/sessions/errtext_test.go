package sessions

import (
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

func TestSessionErrorTextsBothLanguages(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("en") })
	err := i18n.NewError("cli.connection_test.no_password_given")
	i18n.SetLanguage("fr")
	if english, local := errTexts(err); english != "no password given" || local != "aucun mot de passe donné" {
		t.Fatalf("errTexts = %q, %q: want English for agents, French for people", english, local)
	}
	i18n.SetLanguage("en")
	if _, local := errTexts(err); local != "" {
		t.Fatalf("English local text = %q, want empty (Err already is it)", local)
	}
	if english, local := errTexts(nil); english != "" || local != "" {
		t.Fatal("no error, no text")
	}
}

func TestSignInCloneCopiesLocalFailed(t *testing.T) {
	s := Session{SignIn: SignIn{LocalFailed: map[string]string{"k": "v"}}}
	c := s.Clone()
	c.SignIn.LocalFailed["k"] = "changed"
	if s.SignIn.LocalFailed["k"] != "v" {
		t.Fatal("Clone shares LocalFailed")
	}
}
