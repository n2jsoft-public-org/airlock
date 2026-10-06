package dbuser

import "testing"

func TestParseBareUserIsTheIAMEnginesDefault(t *testing.T) {
	d, err := Parse(" app_ro ")
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"postgres", "aurora-postgresql", "mysql", "aurora-mysql", "mariadb"} {
		if got := d.For(engine); got != "app_ro" {
			t.Fatalf("For(%q) = %q, want app_ro", engine, got)
		}
	}
	if got := d.For("sqlserver-se"); got != "" {
		t.Fatalf("For(sqlserver-se) = %q, a bare user must not apply to SQL Server", got)
	}
	if d.String() != "app_ro" {
		t.Fatalf("String = %q", d.String())
	}
}

func TestParseEngineUsers(t *testing.T) {
	d, err := Parse("SQLServer = mcp_ro, app_ro ,mysql=my_ro")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"postgres":          "app_ro",
		"aurora-postgresql": "app_ro",
		"mysql":             "my_ro",
		"aurora-mysql":      "my_ro",
		"sqlserver-ee":      "mcp_ro",
		"sqlserver-ex":      "mcp_ro",
		"oracle-ee":         "",
		"":                  "",
	}
	for engine, want := range cases {
		if got := d.For(engine); got != want {
			t.Fatalf("For(%q) = %q, want %q", engine, got, want)
		}
	}
	if d.String() != "app_ro,mysql=my_ro,sqlserver=mcp_ro" {
		t.Fatalf("String = %q", d.String())
	}
}

func TestParseEngineUserWinsOverBareUser(t *testing.T) {
	d, err := Parse("app_ro,postgres=pg_ro")
	if err != nil {
		t.Fatal(err)
	}
	if d.For("postgres") != "pg_ro" || d.For("mysql") != "app_ro" {
		t.Fatalf("For(postgres) = %q, For(mysql) = %q", d.For("postgres"), d.For("mysql"))
	}
}

func TestParseBlankIsZero(t *testing.T) {
	d, err := Parse("  ")
	if err != nil || !d.IsZero() || d.String() != "" || d.For("postgres") != "" {
		t.Fatalf("Parse(blank) = %+v, %v", d, err)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, in := range []string{
		"oracle=ora_ro",            // unknown engine
		"postgres=a,postgres=b",    // engine twice
		"a,b",                      // two bare users
		"postgres=",                // empty user
		"=app_ro",                  // empty engine
		"app ro",                   // space in a user
		"sqlserver=mcp ro",         // space in an engine user
		"app_ro,,sqlserver=mcp_ro", // empty entry
	} {
		if _, err := Parse(in); err == nil {
			t.Fatalf("Parse(%q) accepted a malformed value", in)
		}
	}
}
