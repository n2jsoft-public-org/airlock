package connections

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCatalogRemembersSortedDatabasesAndForgets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-catalog.json")
	catalog := NewCatalog(path)
	seen := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	catalog.now = func() time.Time { return seen }

	if err := catalog.Remember("111/eu-west-1/rds/main", []string{"orders", "billing", "orders"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Remember("local-db", []string{"app"}, nil); err != nil {
		t.Fatal(err)
	}
	entry := catalog.List()["111/eu-west-1/rds/main"]
	if !slices.Equal(entry.Databases, []string{"billing", "orders"}) || !entry.SeenAt.Equal(seen) {
		t.Fatalf("entry = %+v", entry)
	}

	if err := catalog.Forget("local-db"); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.List()["local-db"]; found {
		t.Fatal("local-db is still in the catalog")
	}
	if len(catalog.List()) != 1 {
		t.Fatalf("catalog = %+v", catalog.List())
	}
}

func TestCatalogReadsAnUnreadableFileAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-catalog.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(path)
	if len(catalog.List()) != 0 {
		t.Fatal("an unreadable catalog must read as empty")
	}
	if err := catalog.Remember("db", []string{"app"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := catalog.List()["db"].Databases; !slices.Equal(got, []string{"app"}) {
		t.Fatalf("databases = %v", got)
	}
}

func TestCatalogKeepsTheTablesOfTheDatabasesStillListed(t *testing.T) {
	catalog := NewCatalog(filepath.Join(t.TempDir(), "database-catalog.json"))
	tables := map[string][]string{"billing": {"public.invoices", "audit.events"}, "orders": {"public.orders"}, "gone": {"public.x"}}
	if err := catalog.Remember("db", []string{"billing", "orders"}, tables); err != nil {
		t.Fatal(err)
	}
	entry := catalog.List()["db"]
	if !slices.Equal(entry.Tables["billing"], []string{"audit.events", "public.invoices"}) || len(entry.Tables) != 2 {
		t.Fatalf("tables = %v", entry.Tables)
	}

	// A listing of the databases alone keeps the tables of those still there.
	if err := catalog.Remember("db", []string{"billing"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := catalog.List()["db"].Tables; len(got) != 1 || !slices.Equal(got["billing"], []string{"audit.events", "public.invoices"}) {
		t.Fatalf("tables = %v", got)
	}
}
