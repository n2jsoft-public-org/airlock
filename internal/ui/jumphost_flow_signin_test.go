package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

// failingKeychainRunner fails to remember any password.
type failingKeychainRunner struct{ *prefsRunner }

func (r *failingKeychainRunner) SaveConnectionPrefs(ctx context.Context, options jumphost.Options) error {
	_ = r.prefsRunner.SaveConnectionPrefs(ctx, options) //nolint:errcheck // test: save succeeds, keychain fails
	return fmt.Errorf("%w: the password was not remembered: %w", jumphost.ErrKeychain, errors.New("locked"))
}

func TestWizardCarriesKeychainWarning(t *testing.T) {
	runner := &failingKeychainRunner{prefsRunner: newPrefsRunner()}
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	m.embedded = true
	m.userInput.SetValue("app_user")
	m.passwordInput.SetValue("s3cret")
	m.remember = true
	m, cmd := wizardUpdate(t, m, key("enter"))
	done, ok := cmd().(jumphostWizardDoneMsg)
	if !ok || done.canceled || !strings.Contains(done.warning, "not remembered") {
		t.Fatalf("done = %+v, want the connection with a keychain warning", done)
	}
}

func TestWizardLeavesSignInUnchosenWhenPasswordAndSecretNotOffered(t *testing.T) {
	cases := []struct {
		target jumphost.ResourceSummary
		name   string
		base   jumphost.Options
	}{
		{jumphost.ResourceSummary{Name: "archive", Engine: "postgres", IAMAuth: true}, "no mcp with iam", jumphost.Options{SkipMCP: true}},
		{jumphost.ResourceSummary{Name: "archive", Engine: "mysql", IAMAuth: true}, "mysql with iam", jumphost.Options{}},
		{jumphost.ResourceSummary{Name: "archive", Engine: "postgres"}, "no mcp", jumphost.Options{SkipMCP: true}},
	}
	for _, c := range cases {
		runner := newPrefsRunner()
		// A remembered password the review cannot offer must survive this connection.
		runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{DBUser: "app_user", SignIn: jumphost.SignInPassword, PasswordInKeychain: true}
		m := wizardAtReviewWithRegion(t, runner, c.base, c.target)
		if _, cmd := wizardUpdate(t, m, key("enter")); !isQuit(cmd) {
			t.Fatalf("%s: wizard did not connect", c.name)
		}
		if got := *runner.lastSave; got.SignIn != "" || got.DBPassword != "" || got.DBSecretARN != "" {
			t.Errorf("%s: saved options = %+v, want no sign-in choice", c.name, got)
		}
	}
}

func TestWizardRecordsIAMChosenAmongPasswordAndSecret(t *testing.T) {
	runner := newPrefsRunner()
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres", IAMAuth: true})
	if _, cmd := wizardUpdate(t, m, key("enter")); !isQuit(cmd) {
		t.Fatal("wizard did not connect")
	}
	if got := *runner.lastSave; got.SignIn != jumphost.SignInIAM {
		t.Fatalf("saved options = %+v, want IAM chosen", got)
	}
}

func TestWizardDropsKeychainPasswordWhenUserChanges(t *testing.T) {
	runner := &keychainPrefsRunner{prefsRunner: newPrefsRunner()}
	runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{DBUser: "app_user", SignIn: jumphost.SignInPassword, PasswordInKeychain: true}
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	m = focusSignIn(t, m)
	m, _ = wizardUpdate(t, m, key("tab")) // user
	if m.reviewFocus != reviewFieldUser || m.passwordInput.Value() != "from-keychain" {
		t.Fatalf("focus = %d password = %q, want the user field and the prefilled password", m.reviewFocus, m.passwordInput.Value())
	}
	m, _ = wizardUpdate(t, m, key("2")) // app_user2
	if m.passwordInput.Value() != "" || !m.remember {
		t.Fatalf("password = %q remember = %v, want the password of app_user dropped, remember kept", m.passwordInput.Value(), m.remember)
	}
	// Typing the password of the new user: moving back to app_user keeps what was typed.
	m, _ = wizardUpdate(t, m, key("tab"))
	m, _ = wizardUpdate(t, m, key("typed"))
	m, _ = wizardUpdate(t, m, key("shift+tab"))
	m, _ = wizardUpdate(t, m, key("backspace"))
	if m.userInput.Value() != "app_user" || m.passwordInput.Value() != "typed" {
		t.Fatalf("user = %q password = %q, want the typed password kept", m.userInput.Value(), m.passwordInput.Value())
	}
}

func TestWizardKeepsKeychainPasswordWhileUserUnchanged(t *testing.T) {
	runner := &keychainPrefsRunner{prefsRunner: newPrefsRunner()}
	runner.saved["eu-west-3/archive"] = jumphost.ConnectionPrefs{DBUser: "app_user", SignIn: jumphost.SignInPassword, PasswordInKeychain: true}
	m := wizardAtReviewWithRegion(t, runner, jumphost.Options{}, jumphost.ResourceSummary{Name: "archive", Engine: "postgres"})
	m = focusSignIn(t, m)
	m, _ = wizardUpdate(t, m, key("tab"))  // user
	m, _ = wizardUpdate(t, m, key("left")) // the cursor moves, the user stays
	m, _ = wizardUpdate(t, m, key("tab"))  // password
	if _, cmd := wizardUpdate(t, m, key("enter")); !isQuit(cmd) {
		t.Fatal("wizard did not connect")
	}
	if got := *runner.lastSave; got.DBUser != "app_user" || got.DBPassword != "from-keychain" || !got.RememberPassword {
		t.Fatalf("saved options: user = %q remember = %v, password kept = %v", got.DBUser, got.RememberPassword, got.DBPassword == "from-keychain")
	}
}

func TestWizardCacheSignInOptionsPerAuthMode(t *testing.T) {
	cases := []struct {
		auth string
		want []string
	}{
		{jumphost.CacheAuthRBAC, []string{jumphost.SignInIAM, jumphost.SignInPassword, jumphost.SignInSecret}},
		{jumphost.CacheAuthToken, []string{jumphost.SignInPassword, jumphost.SignInSecret}},
		{jumphost.CacheAuthNone, nil},
	}
	for _, c := range cases {
		m := wizardAtReviewOfType(t, jumphost.Options{}, "elasticache", jumphost.ResourceSummary{Name: "sessions", Engine: "redis", CacheAuth: c.auth})
		if got := m.signInOptions(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: options = %v, want %v", c.auth, got, c.want)
		}
		if !slices.Contains(m.reviewFields(), reviewFieldMCPWrites) {
			t.Errorf("%s: caches offer the MCP write toggle", c.auth)
		}
	}
}

func TestWizardMemcachedHasNoSignInNorMCP(t *testing.T) {
	m := wizardAtReviewOfType(t, jumphost.Options{}, "elasticache", jumphost.ResourceSummary{Name: "legacy", Engine: "memcached", CacheAuth: jumphost.CacheAuthNone})
	if len(m.signInOptions()) != 0 || m.mcpApplies() || slices.Contains(m.reviewFields(), reviewFieldMCPWrites) {
		t.Fatalf("options = %v, mcp = %v", m.signInOptions(), m.mcpApplies())
	}
}

func TestWizardCacheIAMSuggestsAndRequiresTheUser(t *testing.T) {
	m := wizardAtReviewOfType(t, jumphost.Options{}, "elasticache", jumphost.ResourceSummary{Name: "sessions", Engine: "valkey", CacheAuth: jumphost.CacheAuthRBAC})
	m, _ = wizardUpdate(t, m, jumphostCacheUsersMsg{target: cacheTargetKey(&m.options), users: []string{"app-iam"}})
	if m.signIn != jumphost.SignInIAM || m.userInput.Value() != "app-iam" {
		t.Fatalf("signIn = %q, user = %q; want IAM prefilled with the only IAM user", m.signIn, m.userInput.Value())
	}
	m.userInput.SetValue("")
	if _, field, errMsg := m.applySignIn(); field != reviewFieldUser || !strings.Contains(errMsg, "RBAC user") {
		t.Fatalf("applySignIn = %v %q", field, errMsg)
	}
}

func TestWizardCacheIgnoresTheDefaultDatabaseUser(t *testing.T) {
	// fakeSessionRunner resolves an empty user to testDefaultDBUser, as a configured defaultDbUser does.
	m := wizardAtReviewOfType(t, jumphost.Options{}, "elasticache", jumphost.ResourceSummary{Name: "sessions", Engine: "redis", CacheAuth: jumphost.CacheAuthToken})
	if m.userInput.Value() != "" {
		t.Fatalf("user = %q, the RDS default user must not prefill caches", m.userInput.Value())
	}
}

func TestWizardStaleCacheUsersAreDropped(t *testing.T) {
	m := wizardAtReviewOfType(t, jumphost.Options{}, "elasticache", jumphost.ResourceSummary{Name: "sessions", Engine: "valkey", CacheAuth: jumphost.CacheAuthRBAC})
	other := m.options
	other.Name = "another"
	stale, _ := wizardUpdate(t, m, jumphostCacheUsersMsg{target: cacheTargetKey(&other), users: []string{"old-iam"}})
	if stale.userInput.Value() != "" || stale.userInput.ShowSuggestions {
		t.Fatalf("other target: user = %q, suggestions = %v", stale.userInput.Value(), stale.userInput.ShowSuggestions)
	}
	left := m
	left.step = jumphostStepTarget
	left, _ = wizardUpdate(t, left, jumphostCacheUsersMsg{target: cacheTargetKey(&m.options), users: []string{"old-iam"}})
	if left.userInput.Value() != "" || left.userInput.ShowSuggestions {
		t.Fatalf("outside the review: user = %q, suggestions = %v", left.userInput.Value(), left.userInput.ShowSuggestions)
	}
}

func TestWizardCacheSuggestionsDoNotOutliveTheirTarget(t *testing.T) {
	m := wizardAtReviewOfType(t, jumphost.Options{}, "elasticache", jumphost.ResourceSummary{Name: "sessions", Engine: "valkey", CacheAuth: jumphost.CacheAuthRBAC})
	m, _ = wizardUpdate(t, m, jumphostCacheUsersMsg{target: cacheTargetKey(&m.options), users: []string{"a", "b"}})
	if !m.userInput.ShowSuggestions {
		t.Fatal("suggestions should show for the RBAC cache")
	}
	m.options.ResourceType = "rds"
	m, _ = wizardUpdate(t, m, jumphostDefaultPortLoadedMsg{port: 15443})
	if m.userInput.ShowSuggestions || len(m.userInput.AvailableSuggestions()) != 0 {
		t.Fatalf("suggestions left behind: %v", m.userInput.AvailableSuggestions())
	}
}
