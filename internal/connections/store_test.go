package connections

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

const billingKey = "123456789012/eu-west-3/rds/billing-db"

var billingTarget = JumphostTarget{Account: "123456789012", Region: "eu-west-3", Type: TypeRDS, Target: "billing-db", Profile: "prod"}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreWithClock(filepath.Join(t.TempDir(), "airlock", "saved-connections.json"), func() time.Time { return fixedNow })
}

func writeStoreFile(t *testing.T, store *Store, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readStoreFile(t *testing.T, store *Store) string {
	t.Helper()
	data, err := os.ReadFile(store.Path()) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// withoutDate is c with UsedAt cleared, to compare the other choices with ==.
func withoutDate(c *Choices) Choices {
	result := *c
	result.UsedAt = time.Time{}
	return result
}

func TestRememberJumphostWritesTheEntry(t *testing.T) {
	store := newTestStore(t)
	choices := Choices{User: "app_ro", SignIn: "iam", LocalPort: 5432}
	if err := store.RememberJumphost(billingTarget, choices); err != nil {
		t.Fatal(err)
	}

	entry, found, err := store.Get(billingKey)
	if err != nil || !found || entry.Invalid != nil || entry.Direct != nil || entry.Jumphost == nil || *entry.Jumphost != billingTarget {
		t.Fatalf("entry = %+v, found %v, err %v", entry, found, err)
	}
	if withoutDate(&entry.Choices) != choices || !entry.Choices.UsedAt.Equal(fixedNow) {
		t.Fatalf("choices = %+v, want %+v used at %v", entry.Choices, choices, fixedNow)
	}
	data := readStoreFile(t, store)
	for _, want := range []string{`"version": 1`, `"` + billingKey + `": {`, `"type": "rds"`, `"target": "billing-db"`,
		`"account": "123456789012"`, `"region": "eu-west-3"`, `"profile": "prod"`, `"user": "app_ro"`, `"local_port": 5432`,
		`"used_at": "2026-10-05T09:00:00Z"`} {
		if !strings.Contains(data, want) {
			t.Fatalf("file lacks %s:\n%s", want, data)
		}
	}
	if !strings.HasSuffix(data, "}\n") {
		t.Fatalf("file must end with a newline:\n%q", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("mode = %o, want 600", mode)
		}
	}
}

func TestRememberJumphostRefusesAnIncompleteTarget(t *testing.T) {
	store := newTestStore(t)
	for _, target := range []JumphostTarget{
		{Region: "eu-west-3", Type: TypeRDS, Target: "db"},
		{Account: "1", Type: TypeRDS, Target: "db"},
		{Account: "1", Region: "eu-west-3", Type: "s3", Target: "db"},
		{Account: "1", Region: "eu-west-3", Type: TypeRDS},
		{Account: "1", Region: "eu-west-3", Type: TypeRDS, Target: "a/b"},
	} {
		if err := store.RememberJumphost(target, Choices{}); err == nil {
			t.Fatalf("RememberJumphost(%+v) = nil, want an error", target)
		}
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatalf("nothing must be written: %v", err)
	}
}

const directEntry = `{"engine": "postgres", "host": "analytics.db.internal", "user": "reader", "mcp_allow_writes": false}`

func TestRememberDirectUpdatesTheChoicesOfTheDefinition(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {"analytics": `+directEntry+`}}`)

	choices := Choices{SignIn: "password", PasswordInKeychain: true, MCPAllowWrites: true}
	// A direct database has no user of its own choosing, no secret and no local port.
	if err := store.RememberDirect("analytics", Choices{User: "x", SecretARN: "arn", LocalPort: 1, SignIn: "password", PasswordInKeychain: true, MCPAllowWrites: true}); err != nil {
		t.Fatal(err)
	}
	entry, _, err := store.Get("analytics")
	if err != nil || entry.Direct == nil || entry.Direct.Host != "analytics.db.internal" || entry.Direct.User != "reader" {
		t.Fatalf("entry = %+v, err = %v", entry, err)
	}
	if withoutDate(&entry.Choices) != choices || !entry.Choices.UsedAt.Equal(fixedNow) {
		t.Fatalf("choices = %+v, want %+v", entry.Choices, choices)
	}
}

func TestRememberDirectNeverCreatesAnEntry(t *testing.T) {
	store := newTestStore(t)
	if err := store.RememberDirect("gone-db", Choices{SignIn: "password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatalf("a missing definition must stay missing: %v", err)
	}
}

func TestForgetDeletesAJumphostEntryAndKeepsADirectDefinition(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {"analytics": `+directEntry+`}}`)
	if err := store.RememberDirect("analytics", Choices{SignIn: "password", PasswordInKeychain: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.RememberJumphost(billingTarget, Choices{User: "app_ro"}); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"analytics", billingKey, "missing", billingKey} {
		if err := store.Forget(key); err != nil {
			t.Fatalf("Forget(%s) = %v", key, err)
		}
	}
	if _, found, _ := store.Get(billingKey); found { //nolint:errcheck // test: result is checked by found flag
		t.Fatal("the jumphost entry must be gone")
	}
	entry, found, err := store.Get("analytics")
	if err != nil || !found || entry.Direct == nil || entry.Choices != (Choices{}) {
		t.Fatalf("analytics = %+v (found %v, err %v), want its definition without choices", entry, found, err)
	}
}

func TestInvalidEntryIsIsolatedAndWrittenBackVerbatim(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {
		"analytics": `+directEntry+`,
		"typo-db": {"engine": "postgres", "hots": "typo.db.internal", "user": "reader"}
	}}`)

	entries, err := store.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, err = %v; want both", entries, err)
	}
	if entries[0].Key != "analytics" || entries[0].Invalid != nil || entries[0].Direct == nil {
		t.Fatalf("analytics = %+v, want it valid", entries[0])
	}
	invalid := entries[1]
	if invalid.Key != "typo-db" || invalid.Invalid == nil || invalid.Direct != nil || invalid.Jumphost != nil ||
		!strings.Contains(invalid.Invalid.Error(), `connection "typo-db"`) || !strings.Contains(invalid.Invalid.Error(), "hots") {
		t.Fatalf("typo-db = %+v, want it invalid, naming the entry and the field", invalid)
	}

	if err := store.RememberJumphost(billingTarget, Choices{}); err != nil {
		t.Fatal(err)
	}
	if data := readStoreFile(t, store); !strings.Contains(data, `"hots": "typo.db.internal"`) {
		t.Fatalf("the invalid entry must be written back as it was:\n%s", data)
	}
	if err := store.Forget("typo-db"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get("typo-db"); !found { //nolint:errcheck // test: result is checked by found flag
		t.Fatal("Forget must leave an invalid entry alone")
	}
	if err := store.Delete("typo-db"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get("typo-db"); found { //nolint:errcheck // test: result is checked by found flag
		t.Fatal("Delete must remove an invalid entry")
	}
}

func TestEntryValidation(t *testing.T) {
	cases := []struct {
		name, key, entry, want string
	}{
		{"direct host with scheme", "pg", `{"engine": "postgres", "host": "postgres://db", "user": "u"}`, "host:"},
		{"direct name", "Bad_Name", `{"engine": "postgres", "host": "db", "user": "u"}`, "name:"},
		{"direct with jumphost fields", "pg", `{"engine": "postgres", "host": "db", "user": "u", "type": "rds"}`, "need a jumphost"},
		{"jumphost with direct fields", "1/eu-west-3/rds/db", `{"type": "rds", "target": "db", "host": "db", "jumphost": {"account": "1", "region": "eu-west-3"}}`, "for direct databases"},
		{"jumphost type", "1/eu-west-3/s3/db", `{"type": "s3", "target": "db", "jumphost": {"account": "1", "region": "eu-west-3"}}`, "type:"},
		{"jumphost key", "1/eu-west-3/rds/other", `{"type": "rds", "target": "db", "jumphost": {"account": "1", "region": "eu-west-3"}}`, "the key must be 1/eu-west-3/rds/db"},
		{"jumphost region", "1//rds/db", `{"type": "rds", "target": "db", "jumphost": {"account": "1", "region": ""}}`, "region:"},
		{"null", "pg", `null`, `connection "pg"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			writeStoreFile(t, store, fmt.Sprintf(`{"version": 1, "connections": {%q: %s}}`, tc.key, tc.entry))
			entry, found, err := store.Get(tc.key)
			if err != nil || !found || entry.Invalid == nil || !strings.Contains(entry.Invalid.Error(), tc.want) {
				t.Fatalf("entry = %+v (found %v, err %v), want an invalid entry mentioning %q", entry, found, err, tc.want)
			}
		})
	}
}

func TestUnreadableFileIsNeverRewritten(t *testing.T) {
	for _, content := range []string{
		"{not json",
		`{"version": 2, "connections": {}}`,
		`{"version": 1, "connections": {}} garbage`,
		`{"version": 1, "connections": {}, "extra": true}`,
	} {
		store := newTestStore(t)
		writeStoreFile(t, store, content)
		if _, err := store.List(); err == nil || !strings.Contains(err.Error(), store.Path()) {
			t.Fatalf("List(%q) = %v, want an error naming the file", content, err)
		}
		if err := store.RememberJumphost(billingTarget, Choices{}); err == nil {
			t.Fatalf("RememberJumphost over %q must fail", content)
		}
		if err := store.Delete("x"); err == nil {
			t.Fatalf("Delete over %q must fail", content)
		}
		if got := readStoreFile(t, store); got != content {
			t.Fatalf("file was rewritten: %q", got)
		}
	}
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 2, "connections": {}}`)
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "version 2 is not supported (expected 1): update airlock") {
		t.Fatalf("err = %v, want the version error", err)
	}
}

func TestMissingOrBlankFileHoldsNothing(t *testing.T) {
	store := newTestStore(t)
	if entries, err := store.List(); err != nil || len(entries) != 0 {
		t.Fatalf("missing: entries = %+v, err = %v", entries, err)
	}
	writeStoreFile(t, store, " \n")
	if entries, err := store.List(); err != nil || len(entries) != 0 {
		t.Fatalf("blank: entries = %+v, err = %v", entries, err)
	}
	if err := NewStore("").RememberJumphost(billingTarget, Choices{}); err == nil {
		t.Fatal("a store without path must not write")
	}
}

func TestConcurrentWritersKeepEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate stores, as the daemon and a view are separate processes.
			target := JumphostTarget{Account: "123", Region: "eu-west-3", Type: TypeRDS, Target: fmt.Sprintf("db-%d", i)}
			if err := NewStore(path).RememberJumphost(target, Choices{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := NewStore(path).List()
	if err != nil || len(entries) != 20 {
		t.Fatalf("entries = %d, err = %v; want 20", len(entries), err)
	}
}

func TestFindByKeyOrShortName(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {"analytics": `+directEntry+`}}`)
	other := billingTarget
	other.Region = "eu-west-1"
	cluster := JumphostTarget{Account: "123456789012", Region: "eu-west-3", Type: TypeEKS, Target: "cluster"}
	for _, target := range []JumphostTarget{billingTarget, other, cluster} {
		if err := store.RememberJumphost(target, Choices{}); err != nil {
			t.Fatal(err)
		}
	}

	cases := map[string][]string{
		"analytics":      {"analytics"},
		billingKey:       {billingKey},
		"rds/billing-db": {"123456789012/eu-west-1/rds/billing-db", billingKey},
		"eks/cluster":    {"123456789012/eu-west-3/eks/cluster"},
		"  eks/cluster ": {"123456789012/eu-west-3/eks/cluster"}, //nolint:gocritic // mapKey: whitespace intentional
		"nope":           nil,
	}
	for name, want := range cases {
		matches, err := store.Find(name)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, entry := range matches {
			keys = append(keys, entry.Key)
		}
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Fatalf("Find(%q) = %v, want %v", name, keys, want)
		}
	}
	if (&Entry{Key: billingKey, Jumphost: &billingTarget}).ShortName() != "rds/billing-db" || (&Entry{Key: "analytics"}).ShortName() != "analytics" {
		t.Fatal("ShortName must be <type>/<target> for a jumphost entry, else the key")
	}
}

func TestEntryReadsTheLegacyProductionFlagAndWritesTheEnvironment(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {"billing": {"engine": "postgres", "host": "h", "user": "u", "production": true, "mcp_allow_writes": false}}}`)
	entry, found, err := store.Get("billing")
	if err != nil {
		t.Fatal(err)
	}
	if !found || entry.Direct == nil || entry.Direct.Environment != "prod" {
		t.Fatalf("entry = %+v, %v; want the legacy flag read as prod", entry.Direct, found)
	}
	if err = store.update(func(map[string]Entry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if raw := readStoreFile(t, store); strings.Contains(raw, "production") || !strings.Contains(raw, "environment") {
		t.Fatalf("file = %s, want the environment written instead of the flag", raw)
	}
	entry, found, err = store.Get("billing")
	if err != nil {
		t.Fatal(err)
	}
	if !found || entry.Direct == nil || entry.Direct.Environment != "prod" {
		t.Fatalf("entry = %+v, %v; want prod again after the rewrite", entry.Direct, found)
	}
}
