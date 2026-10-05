package mcpserve

import (
	"path/filepath"
	"testing"
)

func TestServerName(t *testing.T) {
	cases := map[[3]string]string{
		{"acme", "dev", "archive-db"}:         "acme-dev-archive-db",
		{"acme", "ACME Prod", "Sales_DB.v2"}:  "acme-prod-sales-db-v2",
		{"acme", "acme-prod-eu", "orders-db"}: "acme-prod-eu-orders-db",
		{"acme", "acme", "orders-db"}:         "acme-orders-db",
		{"acme", "acmedemo", "orders-db"}:     "acme-acmedemo-orders-db",
		{"acme", "dev", "--weird--name--"}:    "acme-dev-weird-name",
		{"", "dev", "archive-db"}:             "airlock-dev-archive-db",
		{"Team X", "team-x-prod", "db"}:       "team-x-prod-db",
		{"airlock", "airlock", "db"}:          "airlock-db",
	}
	for in, want := range cases {
		if got := ServerName(in[0], in[1], in[2]); got != want {
			t.Fatalf("ServerName(%q, %q, %q) = %q, want %q", in[0], in[1], in[2], got, want)
		}
	}
}

func TestDirectServerName(t *testing.T) {
	cases := map[[2]string]string{
		{"acme", "legacy-billing"}: "acme-legacy-billing",
		{"", "legacy-billing"}:     "airlock-legacy-billing",
		{"acme", "acme-billing"}:   "acme-billing",
		{"acme", "acme"}:           "acme",
		{"acme", "acmedemo"}:       "acme-acmedemo",
		{"Team X", "billing"}:      "team-x-billing",
	}
	for in, want := range cases {
		if got := DirectServerName(in[0], in[1]); got != want {
			t.Fatalf("DirectServerName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestRelativeToShowsProjectAndHomeFilesShort(t *testing.T) {
	home := useFakeHome(t, nil)
	project := filepath.Join(home, "dev", "app")
	got := RelativeTo(project, []string{
		filepath.Join(project, ".mcp.json"),
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(string(filepath.Separator), "etc", "mcp.json"),
	})
	want := ".mcp.json, " + filepath.Join("~", ".codex", "config.toml") + ", " + filepath.Join(string(filepath.Separator), "etc", "mcp.json")
	if got != want {
		t.Fatalf("RelativeTo = %q, want %q", got, want)
	}
}
