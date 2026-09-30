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
	runner := NewRunner(RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "connections.json"), Keychain: store})
	runner.rememberAccount("dev", "111111111111")
	return runner, store
}

func mustSave(t *testing.T, runner *Runner, options *Options) {
	t.Helper()
	if err := runner.SaveConnectionPrefs(context.Background(), *options); err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

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
	mustSave(t, runner, ptr(passwordOptions("app_user", true)))
	if err := runner.SaveConnectionPrefs(context.Background(), passwordOptions("app_user", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry still there: %v", err)
	}
	// Switching to IAM also forgets it.
	mustSave(t, runner, ptr(passwordOptions("app_user", true)))
	iam := passwordOptions("app_user", false)
	iam.SignIn, iam.DBPassword = SignInIAM, ""
	mustSave(t, runner, &iam)
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry kept after switching to IAM: %v", err)
	}
}

func TestSaveConnectionPrefsMovesKeychainEntryWhenUserChanges(t *testing.T) {
	runner, store := keychainRunner(t)
	mustSave(t, runner, ptr(passwordOptions("app_user", true)))
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
	mustSave(t, runner, ptr(passwordOptions("app_user", true)))
	ref := TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "rds", Name: "archive"}
	if err := runner.ForgetConnection(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry kept after forgetting the connection: %v", err)
	}
}

func TestKeychainDisabledWithoutStore(t *testing.T) {
	runner := NewRunner(RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "connections.json")})
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
	runner.forgetKeychainFunc(archiveKeychainAccount)()
	if _, err := store.Get(archiveKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("entry kept: %v", err)
	}
	if runner.forgetKeychainFunc("") != nil {
		t.Fatal("no account must give no callback")
	}
}
