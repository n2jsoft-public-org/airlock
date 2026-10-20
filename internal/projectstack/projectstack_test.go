package projectstack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sample = `{
  "default": "dev",
  "stacks": {
    "dev": [
      { "type": "rds", "name": "orders-db", "port": 5432, "dbUser": "app_ro" },
      { "type": "eks", "name": "platform" },
      { "type": "forward", "host": "search.internal", "remotePort": 443, "port": 9200 },
      { "type": "direct", "name": "legacy-billing" }
    ],
    "staging": { "profile": "stg", "region": "eu-west-1", "targets": [
      { "type": "rds", "name": "orders-db" },
      { "type": "s3", "name": "assets", "profile": "stg-data" },
      { "type": "direct", "name": "billing" }
    ] }
  }
}`

func TestParseReadsStacks(t *testing.T) {
	file, err := Parse("/repo/.airlock.json", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Names(); !reflect.DeepEqual(got, []string{"dev", "staging"}) {
		t.Fatalf("Names() = %v", got)
	}
	name, dev, err := file.Stack("")
	if err != nil || name != "dev" {
		t.Fatalf("Stack(\"\") = %q, %v", name, err)
	}
	want := []Target{
		{Type: "rds", Name: "orders-db", Port: 5432, DBUser: "app_ro"},
		{Type: "eks", Name: "platform"},
		{Type: "forward", Name: "search.internal:443", Port: 9200},
		{Type: "direct", Name: "legacy-billing"},
	}
	if !reflect.DeepEqual(dev, want) {
		t.Fatalf("dev = %+v", dev)
	}
	_, staging, err := file.Stack("staging")
	if err != nil {
		t.Fatal(err)
	}
	want = []Target{
		{Type: "rds", Name: "orders-db", Profile: "stg", Region: "eu-west-1"},
		{Type: "s3", Name: "assets", Profile: "stg-data", Region: "eu-west-1"},
		{Type: "direct", Name: "billing"}, // no AWS profile for a direct database
	}
	if !reflect.DeepEqual(staging, want) {
		t.Fatalf("staging = %+v", staging)
	}
	if _, _, err := file.Stack("prod"); err == nil || !strings.Contains(err.Error(), `no stack "prod"`) {
		t.Fatalf("Stack(prod) error = %v", err)
	}
}

func TestStackWithoutDefault(t *testing.T) {
	one, err := Parse("f", []byte(`{"stacks":{"dev":[{"type":"eks","name":"a"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if name, _, stackErr := one.Stack(""); stackErr != nil || name != "dev" {
		t.Fatalf("only stack: %q, %v", name, stackErr)
	}
	two, err := Parse("f", []byte(`{"stacks":{"dev":[{"type":"eks","name":"a"}],"qa":[{"type":"eks","name":"b"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := two.Stack(""); err == nil || !strings.Contains(err.Error(), "no default") {
		t.Fatalf("two stacks: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	stack := func(targets string) string { return `{"stacks":{"dev":[` + targets + `]}}` }
	tests := []struct{ name, data, want string }{
		{"not json", `{`, "f: invalid JSON"},
		{"top key", `{"mode":"daemon","stacks":{}}`, `unknown key "mode"`},
		{"no stacks", `{"default":"dev"}`, "lists no stacks"},
		{"empty stacks", `{"stacks":{}}`, "lists no stacks"},
		{"stack name", `{"stacks":{"Dev":[{"type":"eks","name":"a"}]}}`, `invalid stack name "Dev"`},
		{"default unknown", `{"default":"qa","stacks":{"dev":[{"type":"eks","name":"a"}]}}`, `default stack "qa"`},
		{"empty stack", stack(``), "f, stack dev: no targets"},
		{"stack shape", `{"stacks":{"dev":"rds"}}`, "expected a list of targets"},
		{"stack key", `{"stacks":{"dev":{"targets":[{"type":"eks","name":"a"}],"hooks":"x"}}}`, `unknown key "hooks"`},
		{"type missing", stack(`{"name":"a"}`), "f, stack dev: target 1: type is required"},
		{"type unknown", stack(`{"type":"ec2","name":"a"}`), `unknown type "ec2"`},
		{"password", stack(`{"type":"rds","name":"a","password":"hunter2"}`), `target 1: "password" looks like a secret`},
		{"token", stack(`{"type":"eks","name":"a","apiToken":"x"}`), `"apiToken" looks like a secret`},
		{"setting", stack(`{"type":"rds","name":"a","mcpClients":"claude"}`), `unknown key "mcpClients" for a rds target`},
		{"key of another type", stack(`{"type":"eks","name":"a","dbUser":"u"}`), `unknown key "dbUser" for a eks target`},
		{"dbSecret on eks", stack(`{"type":"eks","name":"a","dbSecret":"s"}`), `unknown key "dbSecret" for a eks target`},
		{"profile on direct", stack(`{"type":"direct","name":"a","profile":"p"}`), `unknown key "profile" for a direct target`},
		{"key case", stack(`{"type":"rds","Name":"a"}`), `unknown key "Name"`},
		{"name missing", stack(`{"type":"rds","port":5432}`), "name is required"},
		{"bad type of value", stack(`{"type":"rds","name":"a","port":"5432"}`), "target 1: json: cannot unmarshal"},
		{"port range", stack(`{"type":"rds","name":"a","port":70000}`), "port 70000: must be from 1 to 65535"},
		{"remote port missing", stack(`{"type":"forward","host":"h"}`), "remotePort 0"},
		{"forward host", stack(`{"type":"forward","host":"https://h","remotePort":443}`), "without scheme"},
		{"duplicate", stack(`{"type":"eks","name":"a"},{"type":"eks","name":"A"}`), "target 2: eks A is already target 1"},
		{"duplicate port", stack(`{"type":"rds","name":"a","port":5432},{"type":"rds","name":"b","port":5432}`), "target 2: local port 5432 is already taken by target 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("f", []byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(home, "repo")
	deep := filepath.Join(repo, "services", "api")
	for _, dir := range []string{deep, filepath.Join(repo, ".git")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	find := func(dir string) string {
		t.Helper()
		path, err := Find(dir, home)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	// A file above the git root, in home or above it, is never found.
	write(filepath.Join(home, FileName))
	write(filepath.Join(root, FileName))
	if got := find(deep); got != "" {
		t.Fatalf("above the git root: %q", got)
	}
	write(filepath.Join(repo, FileName))
	if got := find(deep); got != filepath.Join(repo, FileName) {
		t.Fatalf("from a subdirectory: %q", got)
	}
	write(filepath.Join(deep, FileName))
	if got := find(deep); got != filepath.Join(deep, FileName) {
		t.Fatalf("nearest: %q", got)
	}
	// Without git, the search stops before home.
	plain := filepath.Join(home, "plain", "sub")
	if err := os.MkdirAll(plain, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := find(plain); got != "" {
		t.Fatalf("home searched: %q", got)
	}
}
