package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
)

// focusSignIn tabs from the port to the sign-in selector.
func focusSignIn(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, _ = wizardUpdate(t, m, key("tab"))
	if m.reviewFocus != reviewFieldSignIn {
		t.Fatalf("focus = %d, want the sign-in selector", m.reviewFocus)
	}
	return m
}

// selectSignIn cycles the focused selector to method.
func selectSignIn(t *testing.T, m jumphostWizardModel, method string) jumphostWizardModel {
	t.Helper()
	for range m.signInOptions() {
		if m.signIn == method {
			return m
		}
		m, _ = wizardUpdate(t, m, key(" "))
	}
	t.Fatalf("sign-in %q not offered: %v", method, m.signInOptions())
	return m
}

func TestWizardSignInOptionsPerTarget(t *testing.T) {
	cases := []struct {
		target jumphost.ResourceSummary
		first  string
		want   []string
	}{
		{jumphost.ResourceSummary{Name: "pg", IAMAuth: true, Engine: "postgres"}, jumphost.SignInIAM,
			[]string{jumphost.SignInIAM, jumphost.SignInPassword, jumphost.SignInSecret}},
		{jumphost.ResourceSummary{Name: "pg", Engine: "postgres"}, jumphost.SignInPassword,
			[]string{jumphost.SignInPassword, jumphost.SignInSecret}},
		{jumphost.ResourceSummary{Name: "mssql", Engine: "sqlserver-se"}, jumphost.SignInAuto,
			[]string{jumphost.SignInAuto, jumphost.SignInPassword, jumphost.SignInSecret}},
	}
	for _, c := range cases {
		m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, c.target)
		if got := m.signInOptions(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: options = %v, want %v", c.target.Engine, got, c.want)
		}
		if m.signIn != c.first {
			t.Errorf("%s: default = %q, want %q", c.target.Engine, m.signIn, c.first)
		}
	}
}

func TestWizardPasswordRememberedInKeychain(t *testing.T) {
	runner := newPrefsRunner()
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	if m.signIn != jumphost.SignInPassword || m.remember {
		t.Fatalf("signIn = %q remember = %v, want password, not remembered by default", m.signIn, m.remember)
	}
	m = focusSignIn(t, m)
	m, _ = wizardUpdate(t, m, key("tab")) // user
	m = clearInput(t, m)
	m, _ = wizardUpdate(t, m, key("app_user"))
	m, _ = wizardUpdate(t, m, key("tab")) // password
	m, _ = wizardUpdate(t, m, key("s3cret"))
	m, _ = wizardUpdate(t, m, key("tab")) // remember
	if m.reviewFocus != reviewFieldRemember || !strings.Contains(m.View(), "Remember") {
		t.Fatalf("focus = %d, want the remember toggle", m.reviewFocus)
	}
	m, _ = wizardUpdate(t, m, key(" "))
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatal("wizard did not connect")
	}
	if got := *runner.lastSave; got.SignIn != jumphost.SignInPassword || !got.RememberPassword || got.DBPassword != "s3cret" || got.DBUser != "app_user" {
		t.Fatalf("saved options = %+v", got)
	}
}

func TestWizardPasswordWithoutKeychainHidesRemember(t *testing.T) {
	runner := &noKeychainRunner{prefsRunner: newPrefsRunner()}
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	for _, field := range m.reviewFields() {
		if field == reviewFieldRemember {
			t.Fatal("remember offered without a keychain")
		}
	}
	if !strings.Contains(m.View(), "No OS keychain") {
		t.Fatal("the review must say why the password cannot be remembered")
	}
	m.userInput.SetValue("app_user")
	m.passwordInput.SetValue("s3cret")
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.RememberPassword || m.options.DBPassword != "s3cret" {
		t.Fatalf("options = %+v, want a password connection without remember", m.options)
	}
}

type noKeychainRunner struct{ *prefsRunner }

func (*noKeychainRunner) KeychainAvailable() bool { return false }

// keychainPrefsRunner remembers one password for app_user.
type keychainPrefsRunner struct{ *prefsRunner }

func (*keychainPrefsRunner) LoadKeychainPassword(_ context.Context, options jumphost.Options) (string, bool) {
	if options.DBUser == "app_user" {
		return "from-keychain", true
	}
	return "", false
}

func TestWizardPrefillsPasswordFromKeychain(t *testing.T) {
	runner := &keychainPrefsRunner{prefsRunner: newPrefsRunner()}
	runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{DBUser: "app_user", SignIn: jumphost.SignInPassword, PasswordInKeychain: true}
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true, Engine: "postgres"})
	if m.signIn != jumphost.SignInPassword || !m.remember || m.passwordInput.Value() != "from-keychain" {
		t.Fatalf("signIn = %q remember = %v password = %q", m.signIn, m.remember, m.passwordInput.Value())
	}
	if strings.Contains(m.View(), "from-keychain") {
		t.Fatal("the prefilled password must stay masked")
	}
}

func TestWizardAutoSignInConnectsWithoutCredentials(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "mssql", Engine: "sqlserver-se"})
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.SignIn != jumphost.SignInAuto || m.options.DBPassword != "" || m.options.DBSecretARN != "" {
		t.Fatalf("options = %+v, want the automatic secrets", m.options)
	}
}

func TestWizardSwitchingSignInDropsTypedPassword(t *testing.T) {
	m := wizardAtReview(t, fakeSessionRunner{}, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", IAMAuth: true, Engine: "postgres"})
	m = focusSignIn(t, m)
	m = selectSignIn(t, m, jumphost.SignInPassword)
	m.passwordInput.SetValue("s3cret")
	m = selectSignIn(t, m, jumphost.SignInIAM)
	m, cmd := wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) || m.options.DBPassword != "" || m.options.SignIn != jumphost.SignInIAM {
		t.Fatalf("options = %+v, want IAM without password", m.options)
	}
}

func TestWizardHidesSignInWhereAirlockDoesNotUseCredentials(t *testing.T) {
	cases := []struct {
		name    string
		base    jumphost.Options
		target  jumphost.ResourceSummary
		options []string
	}{
		{"mysql", jumphost.Options{}, jumphost.ResourceSummary{Name: "my", Engine: "mysql"}, nil},
		{"no engine", jumphost.Options{}, jumphost.ResourceSummary{Name: "my"}, nil},
		{"no mcp", jumphost.Options{SkipMCP: true}, jumphost.ResourceSummary{Name: "pg", Engine: "postgres"}, nil},
		{"mysql with iam", jumphost.Options{}, jumphost.ResourceSummary{Name: "my", Engine: "mysql", IAMAuth: true}, []string{jumphost.SignInIAM}},
		{"no mcp with iam", jumphost.Options{SkipMCP: true}, jumphost.ResourceSummary{Name: "pg", Engine: "postgres", IAMAuth: true}, []string{jumphost.SignInIAM}},
	}
	for _, c := range cases {
		m := wizardAtReview(t, fakeSessionRunner{}, c.base, c.target)
		if got := m.signInOptions(); !reflect.DeepEqual(got, c.options) {
			t.Errorf("%s: options = %v, want %v", c.name, got, c.options)
		}
		if c.options == nil {
			for _, field := range m.reviewFields() {
				if field == reviewFieldSignIn || field == reviewFieldUser {
					t.Errorf("%s: sign-in field %d offered", c.name, field)
				}
			}
			if m.signIn != "" || strings.Contains(m.View(), "Sign-in") {
				t.Errorf("%s: signIn = %q, want no selector", c.name, m.signIn)
			}
			m.userInput.SetValue("someone")
			done, cmd := wizardUpdate(t, m, key("enter"))
			if !isQuit(cmd) || done.options.DBUser != "" || done.options.SignIn != "" {
				t.Errorf("%s: options = %+v, want the sign-in options untouched", c.name, done.options)
			}
		}
	}
}
