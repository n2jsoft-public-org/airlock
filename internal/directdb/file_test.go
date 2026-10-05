package directdb

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDatabasesFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "databases.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadFileMissingFileHasNoDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "databases.json")
	dbs, err := ReadFile(path)
	if err != nil || len(dbs) != 0 {
		t.Fatalf("ReadFile() = %v, %v; want no database", dbs, err)
	}
	if dbs, err := ReadFile(""); err != nil || len(dbs) != 0 {
		t.Fatalf("ReadFile(\"\") = %v, %v; want no database", dbs, err)
	}
}

func TestReadFileReadsEmptyFileAsNoDatabase(t *testing.T) {
	for name, content := range map[string]string{"zero bytes": "", "blank": " \n\t\n"} {
		dbs, err := ReadFile(writeDatabasesFile(t, content))
		if err != nil || len(dbs) != 0 {
			t.Fatalf("%s: ReadFile() = %v, %v; want no database", name, dbs, err)
		}
	}
}

func TestReadFileRejectsInvalidFiles(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    []string
	}{
		"not json":         {`{`, []string{"invalid"}},
		"unknown key":      {`{"version": 1, "databases": {}, "owner": "me"}`, []string{"owner"}},
		"unknown field":    {`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h", "user": "u", "password": "p"}}}`, []string{"password"}},
		"unsupported":      {`{"version": 2, "databases": {}}`, []string{"version 2", "update airlock"}},
		"missing version":  {`{"databases": {}}`, []string{"version 0"}},
		"invalid database": {`{"version": 1, "databases": {"billing": {"engine": "mysql", "host": "h", "user": "u"}}}`, []string{`database "billing"`, "engine: must be postgres or sqlserver"}},
		"invalid name":     {`{"version": 1, "databases": {"Billing": {"engine": "postgres", "host": "h", "user": "u"}}}`, []string{`database "Billing"`, "name:"}},
	} {
		path := writeDatabasesFile(t, tc.content)
		_, err := ReadFile(path)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("%s: err = %v, want the file named", name, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: err = %v, want it to mention %q", name, err, want)
			}
		}
	}
}

func TestNotFoundError(t *testing.T) {
	err := NotFoundError("billing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("NotFoundError() = %v, want it to match ErrNotFound", err)
	}
	if want := "no direct database named billing (see airlock connection list)"; err.Error() != want {
		t.Fatalf("NotFoundError() = %q, want %q", err.Error(), want)
	}
}

func TestReadFileRejectsContentAfterTheJSON(t *testing.T) {
	path := writeDatabasesFile(t, `{"version": 1, "databases": {}} garbage`)
	if _, err := ReadFile(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want an error naming the file", err)
	}
	path = writeDatabasesFile(t, `{"version": 1, "databases": {}}`+"\n\n")
	if _, err := ReadFile(path); err != nil {
		t.Fatalf("trailing whitespace: %v", err)
	}
}
