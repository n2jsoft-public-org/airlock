package mcpserve

import "testing"

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
