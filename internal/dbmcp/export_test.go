package dbmcp

import (
	"context"
	"database/sql/driver"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// exportRows are more rows than query returns, with a text longer than query keeps.
func exportRows() (rows [][]driver.Value, long string) {
	long = strings.Repeat("x", 3*maxTextBytes)
	rows = [][]driver.Value{{int64(0), []byte(long)}, {int64(1), nil}}
	for i := int64(2); i < 1500; i++ {
		rows = append(rows, []driver.Value{i, []byte("v")})
	}
	return rows, long
}

func newExportQuerier(t *testing.T, d *dialect) (*sqlQuerier, *fakeDB) {
	t.Helper()
	q, db, _ := newTestQuerier(d, staticCreds{fallback: "app"})
	q.exportDir = filepath.Join(t.TempDir(), "exports", "acme-dev-app")
	return q, db
}

func TestExportWritesEveryRowUncut(t *testing.T) {
	for _, format := range []string{"", formatJSONL} {
		t.Run(format, func(t *testing.T) {
			q, db := newExportQuerier(t, &postgresDialect)
			rows, long := exportRows()
			db.answer("SELECT id", []string{"id", "note"}, rows...)

			result, err := q.Export(context.Background(), "", "SELECT id, note FROM t", format)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			if result.RowCount != 1500 || result.Truncated || result.Bytes != info.Size() || !slices.Equal(result.Columns, []string{"id", "note"}) {
				t.Fatalf("result = %+v, file size %d", result, info.Size())
			}
			if filepath.Dir(result.Path) != q.exportDir || !strings.Contains(filepath.Base(result.Path), "-app-") {
				t.Fatalf("path = %s, want a file named after the database in %s", result.Path, q.exportDir)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
			}
			if dir, err := os.Stat(q.exportDir); err != nil || dir.Mode().Perm() != 0o700 {
				t.Fatalf("directory = %v, %v; want mode 0700", dir, err)
			}

			content := readFile(t, result.Path)
			var first []string
			if format == formatJSONL {
				lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
				var row map[string]any
				if err := json.Unmarshal([]byte(lines[1]), &row); err != nil || row["note"] != nil || len(lines) != 1500 {
					t.Fatalf("%d lines, second %q: %v", len(lines), lines[1], err)
				}
				first = []string{lines[0]}
			} else {
				records, err := csv.NewReader(strings.NewReader(content)).ReadAll()
				if err != nil || len(records) != 1501 || !slices.Equal(records[0], []string{"id", "note"}) || records[2][1] != "" {
					t.Fatalf("%d records, err %v", len(records), err)
				}
				first = records[1]
			}
			if !strings.Contains(strings.Join(first, ","), long) {
				t.Fatal("the long text was cut")
			}

			wantLog := []string{"BEGIN READ ONLY", "SET LOCAL standard_conforming_strings = on", "SET LOCAL statement_timeout = '30s'",
				"SET LOCAL statement_timeout = '300s'", "SELECT id, note FROM t", "ROLLBACK"}
			if got := db.statements(); !slices.Equal(got, wantLog) {
				t.Fatalf("statements = %q, want %q", got, wantLog)
			}
		})
	}
}

func TestExportPutsBackTheMySQLSessionTimeout(t *testing.T) {
	q, db := newExportQuerier(t, &mysqlDialect)
	db.answer("SELECT 1", []string{"1"}, []driver.Value{int64(1)})

	if _, err := q.Export(context.Background(), "shop", "SELECT 1", formatCSV); err != nil {
		t.Fatal(err)
	}
	wantLog := []string{"BEGIN READ ONLY", "SET SESSION max_execution_time = 300000", "SELECT 1", "SET SESSION max_execution_time = 30000", "ROLLBACK"}
	if got := db.statements(); !slices.Equal(got, wantLog) {
		t.Fatalf("statements = %q, want %q", got, wantLog)
	}
}

func TestExportRefusesWritesAndUnknownFormats(t *testing.T) {
	q, db := newExportQuerier(t, &postgresDialect)

	if _, err := q.Export(context.Background(), "", "DELETE FROM users", formatCSV); err == nil {
		t.Fatal("the read-only guard must refuse DELETE")
	}
	if _, err := q.Export(context.Background(), "", "SELECT 1", "xlsx"); err == nil {
		t.Fatal("xlsx must be refused")
	}
	if got := db.statements(); len(got) != 0 {
		t.Fatalf("statements = %q, want none", got)
	}
	if _, err := os.Stat(q.exportDir); !os.IsNotExist(err) {
		t.Fatalf("export directory created: %v", err)
	}
}

func TestExportStopsAtMaxBytes(t *testing.T) {
	saved := maxExportBytes
	maxExportBytes = 10
	t.Cleanup(func() { maxExportBytes = saved })
	q, db := newExportQuerier(t, &postgresDialect)
	db.answerDraining("SELECT n", []string{"n"}, []driver.Value{[]byte("aaaaaaaaaaaa")}, []driver.Value{[]byte("b")}, []driver.Value{[]byte("c")})

	result, err := q.Export(context.Background(), "", "SELECT n FROM big_table", formatJSONL)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.RowCount != 1 || readFile(t, result.Path) != "{\"n\":\"aaaaaaaaaaaa\"}\n" {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Contains(db.statements(), "rows.Close: context already cancelled, connection left broken") {
		t.Fatalf("the query must be cancelled before rows.Close: %q", db.statements())
	}
}
