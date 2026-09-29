package jumphost

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestPrefsStore(t *testing.T) (store *PrefsStore, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "jumphost", "connections.json")
	store = NewPrefsStore(path)
	store.now = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	return store, path
}

var devArchive = DatabaseRef{Account: "111111111111", Region: "eu-west-3", Name: "archive"}

func TestPrefsStoreRoundTripsPerAccountRegionAndDatabase(t *testing.T) {
	store, path := newTestPrefsStore(t)
	if _, ok := store.Load(devArchive); ok {
		t.Fatal("a missing file must read as no preferences")
	}

	archive := ConnectionPrefs{DBUser: "acme_rw", SignIn: SignInIAM, LocalPort: 5433, MCPAllowWrites: true}
	if err := store.Save(devArchive, archive); err != nil {
		t.Fatal(err)
	}
	otherRegion := DatabaseRef{Account: devArchive.Account, Region: "eu-west-1", Name: "archive"}
	if err := store.Save(otherRegion, ConnectionPrefs{DBUser: "app", SignIn: SignInPassword}); err != nil {
		t.Fatal(err)
	}

	got, ok := store.Load(devArchive)
	archive.UpdatedAt = store.now()
	if !ok || got != archive {
		t.Fatalf("Load = %+v, %v; want %+v", got, ok, archive)
	}
	other, ok := store.Load(otherRegion)
	if !ok || other.DBUser != "app" || other.Passwordless() {
		t.Fatalf("the other region's entry = %+v, %v", other, ok)
	}
	if _, ok := store.Load(DatabaseRef{Account: "222222222222", Region: "eu-west-3", Name: "archive"}); ok {
		t.Fatal("a database of the same name in another account must not share the entry")
	}
	if err := store.Save(DatabaseRef{Region: "eu-west-3", Name: "archive"}, archive); err == nil {
		t.Fatal("saving without an account must fail")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("file mode = %o, want 600", mode)
		}
	}
}

func TestPrefsStoreKeepsUnreadableFile(t *testing.T) {
	store, path := newTestPrefsStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Load(devArchive); ok {
		t.Fatal("an unreadable file must read as no preferences")
	}
	if err := store.Save(devArchive, ConnectionPrefs{}); err == nil {
		t.Fatal("saving must not overwrite a file it cannot parse")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{not json" {
		t.Fatalf("file was rewritten: %q (%v)", data, err)
	}
}

func TestConnectionPrefsDefaultToPasswordless(t *testing.T) {
	if !(ConnectionPrefs{}).Passwordless() {
		t.Fatal("preferences without sign-in method must read as passwordless")
	}
}

func TestRunnerSavesConnectionPrefsWithoutPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	runner := NewRunner(RunnerOptions{PrefsPath: path})
	runner.rememberAccount("dev", "arn:aws:sts::111111111111:assumed-role/AWSReservedSSO_dev/me@example.com")
	options := Options{
		ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3",
		DBUser: "app_user", DBPassword: "s3cret", LocalPortNumber: 5434, MCPAllowWrites: true,
	}

	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), options)
	if !ok || prefs.DBUser != "app_user" || prefs.Passwordless() || prefs.LocalPort != 5434 || !prefs.MCPAllowWrites {
		t.Fatalf("prefs = %+v, %v", prefs, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "s3cret") {
		t.Fatalf("the password must never be saved:\n%s", data)
	}
	if !strings.Contains(string(data), `"111111111111/eu-west-3/archive"`) {
		t.Fatalf("the entry must be keyed by account, region and database:\n%s", data)
	}
}

func TestRememberAccountAcceptsIdentityARNOrAccountID(t *testing.T) {
	runner := NewRunner(RunnerOptions{})
	runner.rememberAccount("dev", "arn:aws:sts::111111111111:assumed-role/role/session\n")
	runner.rememberAccount("prod", "222222222222\n")
	if runner.accounts["dev"] != "111111111111" || runner.accounts["prod"] != "222222222222" {
		t.Fatalf("accounts = %v", runner.accounts)
	}
}

func TestRunnerConnectionPrefsOnlyApplyToRDS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	runner := NewRunner(RunnerOptions{PrefsPath: path})
	options := Options{ResourceType: "eks", Name: "cluster", AwsRegion: "eu-west-3", LocalPortNumber: 9444}

	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an EKS connection must not be saved (stat err = %v)", err)
	}

	disabled := NewRunner(RunnerOptions{})
	rds := Options{ResourceType: "rds", Name: "archive", AwsRegion: "eu-west-3"}
	if err := disabled.SaveConnectionPrefs(context.Background(), rds); err != nil {
		t.Fatal(err)
	}
	if _, ok := disabled.LoadConnectionPrefs(context.Background(), rds); ok {
		t.Fatal("a runner without prefs path must not remember anything")
	}
}
