package sessions_test

import (
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

func TestSessionsErrorsLocalize(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.AWS("archive-db", "rds")
	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "archive-db"}})
	if err != nil {
		t.Fatal(err)
	}
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	_, err = m.Credentials(t.Context(), id)
	if err == nil || err.Error() != "this session does not read its password from Secrets Manager" {
		t.Fatalf("Error() = %v, want English", err)
	}
	if got := i18n.Localize(err); got != "cette session ne lit pas son mot de passe dans Secrets Manager" {
		t.Fatalf("Localize = %q, want French", got)
	}
}

func TestRestartRefusalLocalizes(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.AWS("archive-db", "rds")
	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "archive-db"}})
	if err != nil {
		t.Fatal(err)
	}
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	err = m.Restart(t.Context(), id, nil)
	if err == nil || err.Error() != "archive-db is still running" {
		t.Fatalf("Error() = %v, want English", err)
	}
	if got := i18n.Localize(err); got != "archive-db tourne encore" {
		t.Fatalf("Localize = %q, want French", got)
	}
}
