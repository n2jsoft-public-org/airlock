package jumphost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/n2jsoft/airlock/internal/keychain"
)

const archiveKeychainAccount = "111111111111/eu-west-3/rds/archive/app_user"

func keychainRunner(t *testing.T) (*Runner, *keychain.MemoryStore) {
	t.Helper()
	store := keychain.NewMemoryStore()
	runner := NewRunner(RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "saved-connections.json"), Keychain: store})
	runner.rememberAccount("dev", "111111111111")
	return runner, store
}

func mustSave(t *testing.T, runner *Runner, options *Options) {
	t.Helper()
	if err := runner.SaveConnectionPrefs(context.Background(), *options); err != nil {
		t.Fatal(err)
	}
}

func passwordOptions(user string, remember bool) Options {
	return Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3",
		SignIn: SignInPassword, DBUser: user, DBPassword: "s3cret", RememberPassword: remember}
}

func TestKeychainAccount(t *testing.T) {
	ref := TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "rds", Name: "archive"}
	if got := keychainAccount(ref, " app_user "); got != archiveKeychainAccount {
		t.Fatalf("keychainAccount = %q", got)
	}
}

func TestSaveConnectionPrefsRemembersPasswordInKeychain(t *testing.T) {
	runner, store := keychainRunner(t)
	if err := runner.SaveConnectionPrefs(context.Background(), passwordOptions("app_user", true)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(archiveKeychainAccount); err != nil || got != "s3cret" {
		t.Fatalf("keychain = %q, want the password", got)
	}
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), passwordOptions("app_user", true))
	if !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v, want PasswordInKeychain", prefs)
	}
	got, ok := runner.LoadKeychainPassword(context.Background(), passwordOptions("app_user", false))
	if !ok || got != "s3cret" {
		t.Fatalf("LoadKeychainPassword = %q, %v", got, ok)
	}
}

func TestSaveConnectionPrefsForgetsPasswordWhenNotRemembered(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(passwordOptions("app_user", true)))
	if err := runner.SaveConnectionPrefs(context.Background(), passwordOptions("app_user", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry still there: %v", err)
	}
	// Switching to IAM also forgets it.
	mustSave(t, runner, new(passwordOptions("app_user", true)))
	iam := passwordOptions("app_user", false)
	iam.SignIn, iam.DBPassword = SignInIAM, ""
	mustSave(t, runner, &iam)
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry kept after switching to IAM: %v", err)
	}
}

func TestSaveConnectionPrefsMovesKeychainEntryWhenUserChanges(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(passwordOptions("app_user", true)))
	if err := runner.SaveConnectionPrefs(context.Background(), passwordOptions("other_user", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("entry of the previous user kept: %v", err)
	}
	if got, err := store.Get("111111111111/eu-west-3/rds/archive/other_user"); err != nil || got != "s3cret" {
		t.Fatalf("entry of the new user = %q", got)
	}
}

func TestSaveConnectionPrefsReportsKeychainErrorsButSavesPrefs(t *testing.T) {
	runner, store := keychainRunner(t)
	// Probed while the keychain works; it fails afterwards (e.g. locked).
	if !runner.KeychainAvailable() {
		t.Fatal("the memory keychain must be available")
	}
	store.Err = errors.New("user interaction is not allowed")
	err := runner.SaveConnectionPrefs(context.Background(), passwordOptions("app_user", true))
	if !errors.Is(err, ErrKeychain) {
		t.Fatalf("err = %v, want an ErrKeychain", err)
	}
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), passwordOptions("app_user", true))
	if !ok || prefs.SignInMethod() != SignInPassword || prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v, %v: want saved, without PasswordInKeychain", prefs, ok)
	}
}

func TestForgetConnectionDeletesKeychainEntry(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(passwordOptions("app_user", true)))
	ref := TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "rds", Name: "archive"}
	if err := runner.ForgetConnection(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry kept after forgetting the connection: %v", err)
	}
}

func TestKeychainDisabledWithoutStore(t *testing.T) {
	runner := NewRunner(RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "saved-connections.json")})
	runner.rememberAccount("dev", "111111111111")
	if runner.KeychainAvailable() {
		t.Fatal("no store must not be available")
	}
	if err := runner.SaveConnectionPrefs(context.Background(), passwordOptions("app_user", true)); err != nil {
		t.Fatalf("err = %v, want remember ignored without a keychain", err)
	}
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), passwordOptions("app_user", true))
	if prefs.PasswordInKeychain {
		t.Fatal("PasswordInKeychain saved without a keychain")
	}
}

func TestForgetKeychainFuncDeletesEntry(t *testing.T) {
	runner, store := keychainRunner(t)
	if err := store.Set(archiveKeychainAccount, "wrong"); err != nil {
		t.Fatal(err)
	}
	runner.forgetKeychainFunc(archiveKeychainAccount, "wrong")()
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("entry kept: %v", err)
	}
	if runner.forgetKeychainFunc("", "wrong") != nil {
		t.Fatal("no account must give no callback")
	}
}

func TestForgetKeychainFuncKeepsNewerPassword(t *testing.T) {
	runner, store := keychainRunner(t)
	// A later session remembered the right password after this one read the stale one.
	if err := store.Set(archiveKeychainAccount, "newer"); err != nil {
		t.Fatal(err)
	}
	runner.forgetKeychainFunc(archiveKeychainAccount, "stale")()
	if got, err := store.Get(archiveKeychainAccount); err != nil || got != "newer" {
		t.Fatalf("keychain = %q, %v: want the newer password kept", got, err)
	}
	// A keychain that fails is left alone, without panicking.
	store.Err = errors.New("locked")
	runner.forgetKeychainFunc(archiveKeychainAccount, "newer")()
}

// otherKeychainAccount is the keychain entry of other_user on archive.
const otherKeychainAccount = "111111111111/eu-west-3/rds/archive/other_user"

func TestSaveConnectionPrefsKeepsFlagWhenSetFailsForSameUser(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(passwordOptions("app_user", true)))
	store.Err = errors.New("user interaction is not allowed")
	if err := runner.SaveConnectionPrefs(context.Background(), passwordOptions("app_user", true)); !errors.Is(err, ErrKeychain) {
		t.Fatalf("err = %v, want an ErrKeychain", err)
	}
	store.Err = nil
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), passwordOptions("app_user", true))
	if _, err := store.Get(archiveKeychainAccount); err != nil || !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v, entry err = %v: the old entry is still there, the flag must say so", prefs, err)
	}
}

// unavailableKeychainRunner is a runner sharing the preferences of runner, whose keychain probe fails.
func unavailableKeychainRunner(t *testing.T, runner *Runner) *Runner {
	t.Helper()
	broken := keychain.NewMemoryStore()
	broken.Err = errors.New("no secret service")
	other := NewRunner(RunnerOptions{PrefsPath: runner.prefs.store.Path(), Keychain: broken})
	other.rememberAccount("dev", "111111111111")
	if other.KeychainAvailable() {
		t.Fatal("the broken keychain must be unavailable")
	}
	return other
}

func TestSaveConnectionPrefsKeepsKeychainEntryWhenKeychainUnavailable(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(passwordOptions("app_user", true)))
	other := unavailableKeychainRunner(t, runner)

	// Another user: the entry of app_user cannot be deleted, so the prefs keep pointing at it.
	mustSave(t, other, new(passwordOptions("other_user", true)))
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), passwordOptions("app_user", true))
	if !prefs.PasswordInKeychain || prefs.DBUser != "app_user" {
		t.Fatalf("prefs = %+v, want the flag and the account of the remembered entry kept", prefs)
	}
	// Once the keychain answers again, forgetting the connection removes the entry.
	ref := TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "rds", Name: "archive"}
	if err := runner.ForgetConnection(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry orphaned: %v", err)
	}
	if _, err := store.Get(otherKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("no entry may be written to an unavailable keychain: %v", err)
	}
}

// unchosenSignIn is a connection whose review offered no Password or Secrets Manager choice
// (e.g. --no-mcp): the wizard leaves SignIn empty.
func unchosenSignIn(user string) Options {
	return Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3", DBUser: user}
}

func TestSaveConnectionPrefsKeepsRememberedPasswordWhenSignInNotOffered(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(passwordOptions("app_user", true)))

	mustSave(t, runner, new(unchosenSignIn("iam_user")))
	if got, err := store.Get(archiveKeychainAccount); err != nil || got != "s3cret" {
		t.Fatalf("keychain = %q, %v: a connection without the choice must keep the password", got, err)
	}
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), unchosenSignIn(""))
	if prefs.SignInMethod() != SignInPassword || !prefs.PasswordInKeychain || prefs.DBUser != "app_user" {
		t.Fatalf("prefs = %+v, want the saved password choice kept", prefs)
	}
}

func TestSaveConnectionPrefsKeepsSavedSecretWhenSignInNotOffered(t *testing.T) {
	runner, _ := keychainRunner(t)
	secret := Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3",
		SignIn: SignInSecret, DBSecretARN: "arn:aws:secretsmanager:eu-west-3:111111111111:secret:app-AbCdEf", DBUser: "app_user"}
	mustSave(t, runner, &secret)

	mustSave(t, runner, new(unchosenSignIn("")))
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), unchosenSignIn(""))
	if prefs.SignInMethod() != SignInSecret || prefs.DBSecretARN != secret.DBSecretARN || prefs.DBUser != "app_user" {
		t.Fatalf("prefs = %+v, want the saved secret choice kept", prefs)
	}
}

func TestSaveConnectionPrefsRecordsIAMWhenSignInNotOffered(t *testing.T) {
	runner, _ := keychainRunner(t)
	mustSave(t, runner, new(unchosenSignIn("iam_user")))
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), unchosenSignIn(""))
	if prefs.SignInMethod() != SignInIAM || prefs.DBUser != "iam_user" {
		t.Fatalf("prefs = %+v, want IAM with the user", prefs)
	}
	// A saved IAM choice is not a password: the new user replaces it.
	mustSave(t, runner, new(unchosenSignIn("other_iam_user")))
	prefs, _ = runner.LoadConnectionPrefs(context.Background(), unchosenSignIn(""))
	if prefs.SignInMethod() != SignInIAM || prefs.DBUser != "other_iam_user" {
		t.Fatalf("prefs = %+v, want IAM with the new user", prefs)
	}
}

func cachePasswordOptions(user string) Options {
	return Options{ResourceType: "elasticache", Name: "sessions", AwsProfile: "dev", AwsRegion: "eu-west-3",
		SignIn: SignInPassword, DBUser: user, DBPassword: "t0ken", RememberPassword: true, MCPAllowWrites: true}
}

func TestSaveConnectionPrefsRemembersCachePasswordAsTheDefaultUser(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(cachePasswordOptions("")))
	if got, err := store.Get("111111111111/eu-west-3/elasticache/sessions/default"); err != nil || got != "t0ken" {
		t.Fatalf("keychain = %q, %v; want the AUTH token under the default user", got, err)
	}
	reload := Options{ResourceType: "elasticache", Name: "sessions", AwsProfile: "dev", AwsRegion: "eu-west-3"}
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), reload)
	if !ok || prefs.SignInMethod() != SignInPassword || prefs.DBUser != "default" || !prefs.PasswordInKeychain || !prefs.MCPAllowWrites {
		t.Fatalf("prefs = %+v, %v; want the password method, the default user, remembered, with writes", prefs, ok)
	}
	// The wizard asks for the password of the saved user; an empty user also means "default".
	for _, user := range []string{"", "default"} {
		reload.DBUser = user
		if got, ok := runner.LoadKeychainPassword(context.Background(), reload); !ok || got != "t0ken" {
			t.Fatalf("LoadKeychainPassword(user %q) = %q, %v", user, got, ok)
		}
	}
}

func TestSaveConnectionPrefsRemembersCacheRBACPassword(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(cachePasswordOptions("app")))
	if got, err := store.Get("111111111111/eu-west-3/elasticache/sessions/app"); err != nil || got != "t0ken" {
		t.Fatalf("keychain = %q, %v", got, err)
	}
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), cachePasswordOptions("app"))
	if prefs.DBUser != "app" || !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v", prefs)
	}
}

func TestSaveConnectionPrefsRemembersCacheSecret(t *testing.T) {
	runner, _ := keychainRunner(t)
	options := Options{ResourceType: "elasticache", Name: "sessions", AwsProfile: "dev", AwsRegion: "eu-west-3",
		SignIn: SignInSecret, DBSecretARN: "arn:aws:secretsmanager:eu-west-3:111111111111:secret:sessions-auth"}
	mustSave(t, runner, &options)
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), options)
	if !ok || prefs.SignInMethod() != SignInSecret || prefs.DBSecretARN != options.DBSecretARN {
		t.Fatalf("prefs = %+v, %v; want the secret method and ARN", prefs, ok)
	}
}

func TestKeychainAccountOfAnEmptyCacheUserIsTheDefaultUser(t *testing.T) {
	ref := TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "elasticache", Name: "sessions"}
	if got := keychainAccount(ref, " "); got != "111111111111/eu-west-3/elasticache/sessions/default" {
		t.Fatalf("keychainAccount = %q", got)
	}
}

// undeletableKeychain is a keychain whose entries cannot be deleted.
type undeletableKeychain struct{ *keychain.MemoryStore }

func (undeletableKeychain) Delete(string) error { return errors.New("keychain locked") }

func TestForgetConnectionKeepsThePrefsWhenTheKeychainDeleteFails(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, new(passwordOptions("app_user", true)))
	ref := TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "rds", Name: "archive"}

	runner.keychain = undeletableKeychain{store}
	if err := runner.ForgetConnection(context.Background(), ref); !errors.Is(err, ErrKeychain) {
		t.Fatalf("err = %v, want ErrKeychain", err)
	}
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), passwordOptions("app_user", true))
	if !ok || !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v, %v: want the password still flagged so a retry deletes it", prefs, ok)
	}

	runner.keychain = store
	if err := runner.ForgetConnection(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry kept after the retry: %v", err)
	}
}
