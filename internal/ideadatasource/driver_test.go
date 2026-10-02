package ideadatasource

import "testing"

func TestPostgresURLEncodesTheDatabase(t *testing.T) {
	for database, want := range map[string]string{
		"billing":  "jdbc:postgresql://db:5432/billing",
		"":         "jdbc:postgresql://db:5432/",
		"my db#1%": "jdbc:postgresql://db:5432/my+db%231%25",
	} {
		if got := postgresDriver.URL("db", 5432, database); got != want {
			t.Errorf("URL(%q) = %q, want %q", database, got, want)
		}
	}
}

func TestSQLServerURLBracesSpecialDatabaseNames(t *testing.T) {
	for database, want := range map[string]string{
		"Billing":  "jdbc:sqlserver://db:1433;databaseName=Billing",
		"":         "jdbc:sqlserver://db:1433",
		"My DB":    "jdbc:sqlserver://db:1433;databaseName={My DB}",
		"a}b#1%":   "jdbc:sqlserver://db:1433;databaseName={a}}b#1%}",
		"sales_v2": "jdbc:sqlserver://db:1433;databaseName=sales_v2",
	} {
		if got := sqlServerDriver.URL("db", 1433, database); got != want {
			t.Errorf("URL(%q) = %q, want %q", database, got, want)
		}
	}
}
