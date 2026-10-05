package jumphost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

var devArchive = TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "rds", Name: "archive"}

func TestPrefsStoreRoundTripsPerAccountRegionAndDatabase(t *testing.T) {
	store, path := newTestPrefsStore(t)
	if _, ok := store.Load(devArchive); ok {
		t.Fatal("a missing file must read as no preferences")
	}

	archive := ConnectionPrefs{DBUser: "acme_rw", SignIn: SignInIAM, LocalPort: 5433, MCPAllowWrites: true}
	if err := store.Save(devArchive, &archive); err != nil {
		t.Fatal(err)
	}
	otherRegion := TargetRef{Account: devArchive.Account, Region: "eu-west-1", Type: "rds", Name: "archive"}
	if err := store.Save(otherRegion, &ConnectionPrefs{DBUser: "app", SignIn: SignInPassword}); err != nil {
		t.Fatal(err)
	}

	got, ok := store.Load(devArchive)
	archive.UpdatedAt = store.now()
	if !ok || got != archive {
		t.Fatalf("Load = %+v, %v; want %+v", got, ok, archive)
	}
	other, ok := store.Load(otherRegion)
	if !ok || other.DBUser != "app" || other.SignInMethod() != SignInPassword {
		t.Fatalf("the other region's entry = %+v, %v", other, ok)
	}
	if _, ok := store.Load(TargetRef{Account: "222222222222", Region: "eu-west-3", Type: "rds", Name: "archive"}); ok {
		t.Fatal("a database of the same name in another account must not share the entry")
	}
	if err := store.Save(TargetRef{Region: "eu-west-3", Type: "rds", Name: "archive"}, &archive); err == nil {
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
	if err := store.Save(devArchive, &ConnectionPrefs{}); err == nil {
		t.Fatal("saving must not overwrite a file it cannot parse")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{not json" {
		t.Fatalf("file was rewritten: %q (%v)", data, err)
	}
}

func TestPrefsStoreKeysByTypeAndWritesVersion2(t *testing.T) {
	store, path := newTestPrefsStore(t)
	cache := TargetRef{Account: devArchive.Account, Region: devArchive.Region, Type: "elasticache", Name: "archive"}
	if err := store.Save(devArchive, &ConnectionPrefs{Profile: "dev", ResourceType: "rds", LocalPort: 5433}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(cache, &ConnectionPrefs{Profile: "dev", ResourceType: "elasticache", LocalPort: 6380}); err != nil {
		t.Fatal(err)
	}

	db, _ := store.Load(devArchive)
	redis, _ := store.Load(cache)
	if db.LocalPort != 5433 || redis.LocalPort != 6380 {
		t.Fatalf("a database and a cache of the same name must not share an entry: %+v %+v", db, redis)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is inside the test temp directory.
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"111111111111/eu-west-3/rds/archive"`, `"111111111111/eu-west-3/elasticache/archive"`, `"version": 2`, `"profile": "dev"`, `"resource_type": "rds"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("file lacks %s:\n%s", want, data)
		}
	}
}

func TestPrefsStoreListsNewestFirstAndSkipsMalformedKeys(t *testing.T) {
	store, path := newTestPrefsStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"version": 2, "connections": {
		"111111111111/eu-west-3/rds/old": {"updated_at": "2026-09-01T00:00:00Z", "profile": "dev"},
		"111111111111/eu-west-3/eks/new": {"updated_at": "2026-09-20T00:00:00Z", "profile": "dev"},
		"eu-west-3/leftover": {"updated_at": "2026-09-25T00:00:00Z"},
		"111111111111//rds/no-region": {"updated_at": "2026-09-25T00:00:00Z"}
	}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	saved, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[0].Ref.Name != "new" || saved[1].Ref.Name != "old" {
		t.Fatalf("List = %+v, want new then old", saved)
	}
	if saved[0].Ref.Type != "eks" || saved[0].Ref.Account != "111111111111" || saved[0].Prefs.Profile != "dev" {
		t.Fatalf("first entry = %+v", saved[0])
	}
}

func TestPrefsStoreDeleteKeepsOtherEntries(t *testing.T) {
	store, _ := newTestPrefsStore(t)
	other := TargetRef{Account: devArchive.Account, Region: devArchive.Region, Type: "eks", Name: "cluster"}
	for _, ref := range []TargetRef{devArchive, other} {
		if err := store.Save(ref, &ConnectionPrefs{Profile: "dev"}); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Delete(devArchive); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(devArchive); err != nil {
		t.Fatalf("deleting a missing entry must not fail: %v", err)
	}
	saved, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Ref != other {
		t.Fatalf("List = %+v, want only %+v", saved, other)
	}
}

func TestPrefsStoreListAndDeleteRefuseUnreadableFile(t *testing.T) {
	store, path := newTestPrefsStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("List must report a file it cannot parse")
	}
	if err := store.Delete(devArchive); err == nil {
		t.Fatal("Delete must not rewrite a file it cannot parse")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is inside the test temp directory.
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{not json" {
		t.Fatalf("file was rewritten: %q", data)
	}
}

func TestConnectionPrefsSignInMethodDefaultsToIAM(t *testing.T) {
	if got := (ConnectionPrefs{}).SignInMethod(); got != SignInIAM {
		t.Fatalf("SignInMethod() = %q, want %q", got, SignInIAM)
	}
	if got := (ConnectionPrefs{SignIn: SignInSecret}).SignInMethod(); got != SignInSecret {
		t.Fatalf("SignInMethod() = %q, want %q", got, SignInSecret)
	}
}

func TestOptionsSignInMethod(t *testing.T) {
	cases := []struct { //nolint:govet // test struct
		options Options
		want    string
	}{
		{Options{}, SignInIAM},
		{Options{DBPassword: "s3cret"}, SignInPassword},
		{Options{DBSecretARN: "arn:secret"}, SignInSecret},
		{Options{SignIn: SignInAuto}, SignInAuto},
	}
	for _, c := range cases {
		if got := c.options.SignInMethod(); got != c.want {
			t.Errorf("%+v.SignInMethod() = %q, want %q", c.options, got, c.want)
		}
	}
}

func TestPrefsStoreRoundTripsSecretAndKeychainChoices(t *testing.T) {
	store, path := newTestPrefsStore(t)
	prefs := ConnectionPrefs{DBUser: "app_user", SignIn: SignInSecret, DBSecretARN: "arn:aws:secretsmanager:eu-west-3:111111111111:secret:app-AbCdEf"}
	if err := store.Save(devArchive, &prefs); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Load(devArchive)
	prefs.UpdatedAt = store.now()
	if !ok || got != prefs {
		t.Fatalf("Load = %+v, %v", got, ok)
	}
	//nolint:errcheck // test file, ignoring read error
	data, _ := os.ReadFile(path) // #nosec G304
	if !strings.Contains(string(data), `"db_secret_arn"`) || !strings.Contains(string(data), `"version": 2`) {
		t.Fatalf("file = %s", data)
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
	if !ok || prefs.DBUser != "app_user" || prefs.SignInMethod() != SignInPassword || prefs.LocalPort != 5434 || !prefs.MCPAllowWrites {
		t.Fatalf("prefs = %+v, %v", prefs, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "s3cret") {
		t.Fatalf("the password must never be saved:\n%s", data)
	}
	if !strings.Contains(string(data), `"111111111111/eu-west-3/rds/archive"`) {
		t.Fatalf("the entry must be keyed by account, region, type and name:\n%s", data)
	}
}

func TestRunnerSavesSecretChoice(t *testing.T) {
	runner := NewRunner(RunnerOptions{PrefsPath: filepath.Join(t.TempDir(), "connections.json")})
	runner.rememberAccount("dev", "111111111111")
	options := Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3",
		DBSecretARN: "arn:secret", SignIn: SignInSecret}
	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	prefs, _ := runner.LoadConnectionPrefs(context.Background(), options)
	if prefs.SignInMethod() != SignInSecret || prefs.DBSecretARN != "arn:secret" || prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v", prefs)
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

func TestRunnerSavesEKSConnectionWithoutDatabaseFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	runner := NewRunner(RunnerOptions{PrefsPath: path})
	runner.rememberAccount("dev", "111111111111")
	options := Options{
		ResourceType: "eks", Name: "cluster", AwsProfile: "dev", AwsRegion: "eu-west-3",
		LocalPortNumber: 9444, DBUser: "ignored", MCPAllowWrites: true,
	}

	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), options)
	want := ConnectionPrefs{UpdatedAt: prefs.UpdatedAt, Profile: "dev", ResourceType: "eks", LocalPort: 9444}
	if !ok || prefs != want {
		t.Fatalf("prefs = %+v, %v; want %+v", prefs, ok, want)
	}
}

func TestRunnerSavesProfileAndTypeOfRDSConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	runner := NewRunner(RunnerOptions{PrefsPath: path})
	runner.rememberAccount("dev", "111111111111")
	options := Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3", DBUser: "app"}

	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	prefs, ok := runner.LoadConnectionPrefs(context.Background(), options)
	if !ok || prefs.Profile != "dev" || prefs.ResourceType != "rds" || prefs.DBUser != "app" {
		t.Fatalf("prefs = %+v, %v", prefs, ok)
	}
}

func TestRunnerWithoutPrefsPathRemembersNothing(t *testing.T) {
	disabled := NewRunner(RunnerOptions{})
	rds := Options{ResourceType: "rds", Name: "archive", AwsRegion: "eu-west-3"}
	if err := disabled.SaveConnectionPrefs(context.Background(), rds); err != nil {
		t.Fatal(err)
	}
	if _, ok := disabled.LoadConnectionPrefs(context.Background(), rds); ok {
		t.Fatal("a runner without prefs path must not remember anything")
	}
	saved, err := disabled.ListSavedConnections(context.Background(), "", "")
	if err != nil || len(saved) != 0 {
		t.Fatalf("ListSavedConnections = %+v, %v; want none", saved, err)
	}
	if err := disabled.ForgetConnection(context.Background(), TargetRef{}); err != nil {
		t.Fatal(err)
	}
}

func TestPrefsStoreConcurrentSavesKeepEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate stores, as the daemon and a view are separate processes.
			store := NewPrefsStore(path)
			ref := TargetRef{Account: "123", Region: "eu-west-3", Type: "rds", Name: fmt.Sprintf("db-%d", i)}
			if err := store.Save(ref, &ConnectionPrefs{Profile: "dev"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	saved, err := NewPrefsStore(path).List()
	if err != nil || len(saved) != 20 {
		t.Fatalf("saved = %d entries, err = %v; want 20", len(saved), err)
	}
}
