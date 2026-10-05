package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
)

var pickerSecrets = []jumphost.SecretSummary{
	{ARN: "arn:linked", Name: "prod/archive/app-user", Linked: true},
	{ARN: "arn:other", Name: "team/other", Description: "billing reports"},
}

func TestFilterSecretsMatchesEveryWordInNameOrDescription(t *testing.T) {
	if got := filterSecrets(pickerSecrets, ""); len(got) != 2 {
		t.Fatalf("empty query = %+v", got)
	}
	if got := filterSecrets(pickerSecrets, "ARCHIVE user"); len(got) != 1 || got[0].ARN != "arn:linked" {
		t.Fatalf("name query = %+v", got)
	}
	if got := filterSecrets(pickerSecrets, "billing"); len(got) != 1 || got[0].ARN != "arn:other" {
		t.Fatalf("description query = %+v", got)
	}
}

func TestSecretPickerLoadsFiltersAndPicks(t *testing.T) {
	p := newSecretPicker("eu-west-3")
	if _, ok := p.choice(); ok {
		t.Fatal("nothing can be picked while loading")
	}
	if !strings.Contains(p.View(NewStyles(true), 100, 30), "Loading") {
		t.Fatal("the picker must show it is loading")
	}
	p = p.loaded(pickerSecrets, nil)
	view := p.View(NewStyles(true), 100, 30)
	if !strings.Contains(view, "prod/archive/app-user") || !strings.Contains(view, "linked") {
		t.Fatalf("view misses the linked secret:\n%s", view)
	}

	p, _, _ = p.Update(key("other"))
	p, outcome, _ := p.Update(key("enter"))
	if outcome != secretPickerChosen {
		t.Fatalf("outcome = %v, want chosen", outcome)
	}
	if got, ok := p.choice(); !ok || got.ARN != "arn:other" {
		t.Fatalf("choice = %+v, %v", got, ok)
	}
	if _, outcome, _ = p.Update(key("esc")); outcome != secretPickerCanceled {
		t.Fatalf("esc outcome = %v", outcome)
	}
}

func TestSecretPickerShowsErrors(t *testing.T) {
	p := newSecretPicker("eu-west-3").loaded(nil, errors.New("failed to list the secrets (missing secretsmanager:ListSecrets?)"))
	if view := p.View(NewStyles(true), 100, 30); !strings.Contains(view, "secretsmanager:ListSecrets") {
		t.Fatalf("view misses the error:\n%s", view)
	}
	if _, outcome, _ := p.Update(key("enter")); outcome != secretPickerOpen {
		t.Fatalf("enter on an error = %v, want the picker kept open", outcome)
	}
}

func TestWizardPicksSecretAndConnects(t *testing.T) {
	runner := newPrefsRunner()
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	m = focusSignIn(t, m)
	m = selectSignIn(t, m, jumphost.SignInSecret)
	m, _ = wizardUpdate(t, m, key("tab"))
	if m.reviewFocus != reviewFieldSecret {
		t.Fatalf("focus = %d, want the secret field", m.reviewFocus)
	}

	m, cmd := wizardUpdate(t, m, key("enter"))
	if !m.pickingSecret {
		t.Fatal("enter on Secret did not open the picker")
	}
	m, _ = wizardUpdate(t, m, findMsg[jumphostSecretsLoadedMsg](t, cmd))
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.pickingSecret || m.secret.ARN != "arn:linked" {
		t.Fatalf("picking = %v secret = %+v, want the linked secret picked", m.pickingSecret, m.secret)
	}

	m, cmd = wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.DBSecretARN != "arn:linked" || m.options.SignIn != jumphost.SignInSecret || m.options.DBPassword != "" {
		t.Fatalf("options = %+v, want the picked secret", m.options)
	}
}

func TestWizardSecretRequiredBeforeConnecting(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	m = focusSignIn(t, m)
	m = selectSignIn(t, m, jumphost.SignInSecret)
	m, _ = wizardUpdate(t, m, key("shift+tab"))
	m, cmd := wizardUpdate(t, m, key("enter")) // on the port: submit
	if isQuit(cmd) || m.reviewFocus != reviewFieldSecret || !strings.Contains(m.errorMsg, "secret") {
		t.Fatalf("quit = %v focus = %d error = %q", isQuit(cmd), m.reviewFocus, m.errorMsg)
	}
}

func TestWizardPrefillsSavedSecret(t *testing.T) {
	runner := newPrefsRunner()
	runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{SignIn: jumphost.SignInSecret,
		DBSecretARN: "arn:aws:secretsmanager:eu-west-3:111:secret:prod/app-user-AbC123"}
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	if m.signIn != jumphost.SignInSecret || m.secret.Name != "prod/app-user" || !strings.Contains(m.render(), "prod/app-user") {
		t.Fatalf("signIn = %q secret = %+v", m.signIn, m.secret)
	}
}

func TestWizardSecretFlagSeedsReview(t *testing.T) {
	arn := "arn:aws:secretsmanager:eu-west-3:111:secret:prod/app-user-AbC123"
	runner := newPrefsRunner()
	runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{SignIn: jumphost.SignInPassword, DBUser: "saved"}
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{DBSecretARN: arn}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	if m.signIn != jumphost.SignInSecret || m.secret.ARN != arn || !strings.Contains(m.render(), "prod/app-user") {
		t.Fatalf("signIn = %q secret = %+v", m.signIn, m.secret)
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.DBSecretARN != arn || m.options.SignIn != jumphost.SignInSecret {
		t.Fatalf("options = %+v, want the flag secret", m.options)
	}
}
