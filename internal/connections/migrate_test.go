package connections

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const legacyDatabases = `{"version": 1, "databases": {
	"legacy-billing": {"engine": "sqlserver", "host": "billing.db.internal", "user": "mcp_ro", "production": true},
	"analytics": {"engine": "postgres", "host": "10.0.4.12", "database": "events", "user": "reader", "tls": "verify"}
}}`

const legacyConnections = `{"version": 2, "connections": {
	"local/local/direct/legacy-billing": {"updated_at": "2026-10-01T08:00:00Z", "resource_type": "direct", "db_user": "mcp_ro", "sign_in": "password", "password_in_keychain": true, "mcp_allow_writes": true},
	"local/local/direct/gone-db": {"updated_at": "2026-09-01T08:00:00Z", "resource_type": "direct", "db_user": "x"},
	"123456789012/eu-west-3/rds/billing-db": {"updated_at": "2026-10-04T17:40:00Z", "profile": "prod", "resource_type": "rds", "db_user": "app_ro", "sign_in": "secret", "db_secret_arn": "arn:aws:secretsmanager:eu-west-3:123456789012:secret:ro", "local_port": 5432, "mcp_allow_writes": false},
	"123456789012/eu-west-3/eks/cluster": {"updated_at": "2026-10-03T10:00:00Z", "profile": "prod", "resource_type": "eks", "local_port": 9444, "mcp_allow_writes": false},
	"eu-west-3/leftover": {"updated_at": "2026-09-25T00:00:00Z"}
}}`

// legacyFiles writes the old files next to the store's and returns their paths; an empty content
// leaves the file out.
func legacyFiles(t *testing.T, store *Store, databases, connections string) (databasesPath, connectionsPath string) {
	t.Helper()
	dir := filepath.Dir(store.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	databasesPath, connectionsPath = filepath.Join(dir, "databases.json"), filepath.Join(dir, "connections.json")
	for path, content := range map[string]string{databasesPath: databases, connectionsPath: connections} {
		if content == "" {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return databasesPath, connectionsPath
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestMigrateMovesBothFiles(t *testing.T) {
	store := newTestStore(t)
	databasesPath, connectionsPath := legacyFiles(t, store, legacyDatabases, legacyConnections)

	migrated, err := store.Migrate(databasesPath, connectionsPath)
	if err != nil || !migrated {
		t.Fatalf("Migrate = %v, %v; want true", migrated, err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, entry := range entries {
		if entry.Invalid != nil {
			t.Fatalf("invalid entry after migration: %v", entry.Invalid)
		}
		keys = append(keys, entry.Key)
	}
	if got := strings.Join(keys, ","); got != "123456789012/eu-west-3/eks/cluster,123456789012/eu-west-3/rds/billing-db,analytics,legacy-billing" {
		t.Fatalf("keys = %s; want the two definitions and the two AWS targets, without gone-db nor leftover", got)
	}

	billing, _, err := store.Get("legacy-billing") //nolint:errcheck // test only
	if err != nil {
		t.Fatal(err)
	}
	if billing.Direct == nil || !billing.Direct.Production || billing.Direct.User != "mcp_ro" ||
		billing.Choices.SignIn != "password" || !billing.Choices.PasswordInKeychain || !billing.Choices.MCPAllowWrites ||
		!billing.Choices.UsedAt.Equal(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("legacy-billing = %+v / %+v, want its definition with its choices", billing.Direct, billing.Choices)
	}
	analytics, _, err := store.Get("analytics") //nolint:errcheck // test only
	if err != nil {
		t.Fatal(err)
	}
	if analytics.Direct == nil || analytics.Direct.Database != "events" || analytics.Choices != (Choices{}) {
		t.Fatalf("analytics = %+v, want its definition, never opened", analytics)
	}
	rds, _, err := store.Get("123456789012/eu-west-3/rds/billing-db") //nolint:errcheck // test only
	if err != nil {
		t.Fatal(err)
	}
	if rds.Jumphost == nil || rds.Jumphost.Profile != "prod" || rds.Choices.User != "app_ro" || rds.Choices.SignIn != "secret" ||
		rds.Choices.SecretARN != "arn:aws:secretsmanager:eu-west-3:123456789012:secret:ro" || rds.Choices.LocalPort != 5432 {
		t.Fatalf("rds = %+v / %+v", rds.Jumphost, rds.Choices)
	}

	for _, path := range []string{databasesPath, connectionsPath} {
		if exists(path) || !exists(path+".bak") {
			t.Fatalf("%s must be renamed with .bak", path)
		}
	}
	if again, err := store.Migrate(databasesPath, connectionsPath); err != nil || again {
		t.Fatalf("second Migrate = %v, %v; want nothing done", again, err)
	}
}

func TestMigrateNeverMergesIntoAnExistingFile(t *testing.T) {
	store := newTestStore(t)
	databasesPath, connectionsPath := legacyFiles(t, store, legacyDatabases, legacyConnections)
	writeStoreFile(t, store, `{"version": 1, "connections": {}}`)

	if migrated, err := store.Migrate(databasesPath, connectionsPath); err != nil || migrated {
		t.Fatalf("Migrate = %v, %v; want nothing done", migrated, err)
	}
	if !exists(databasesPath) || !exists(connectionsPath) {
		t.Fatal("the old files must be left alone")
	}
}

func TestMigrateWithoutOldFiles(t *testing.T) {
	store := newTestStore(t)
	databasesPath, connectionsPath := legacyFiles(t, store, "", "")
	if migrated, err := store.Migrate(databasesPath, connectionsPath); err != nil || migrated {
		t.Fatalf("Migrate = %v, %v; want nothing done", migrated, err)
	}
	if exists(store.Path()) {
		t.Fatal("nothing to migrate must write nothing")
	}
}

func TestMigrateWithOnlyConnections(t *testing.T) {
	store := newTestStore(t)
	databasesPath, connectionsPath := legacyFiles(t, store, "", legacyConnections)
	if migrated, err := store.Migrate(databasesPath, connectionsPath); err != nil || !migrated {
		t.Fatalf("Migrate = %v, %v; want true", migrated, err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, %v; want the two AWS targets (direct choices have no definition)", entries, err)
	}
	if exists(databasesPath + ".bak") {
		t.Fatal("a missing file must not get a .bak")
	}
}

func TestMigrateStopsOnAnUnreadableOldFile(t *testing.T) {
	for name, files := range map[string][2]string{
		"databases":   {`{"version": 1, "databases": {"bad": {"engine": "mysql"}}}`, legacyConnections},
		"connections": {legacyDatabases, "{not json"},
	} {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			databasesPath, connectionsPath := legacyFiles(t, store, files[0], files[1])
			migrated, err := store.Migrate(databasesPath, connectionsPath)
			bad := databasesPath
			if name == "connections" {
				bad = connectionsPath
			}
			if migrated || err == nil || !strings.Contains(err.Error(), bad) {
				t.Fatalf("Migrate = %v, %v; want an error naming %s", migrated, err, bad)
			}
			if exists(store.Path()) || !exists(databasesPath) || !exists(connectionsPath) {
				t.Fatal("nothing must be written nor renamed")
			}
		})
	}
}
