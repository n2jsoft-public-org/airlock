package directdb

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/sharecode"
)

func TestExportImportRoundTrip(t *testing.T) {
	billing := Database{Name: "legacy-billing", Engine: EngineSQLServer, Host: "billing.db.internal", User: "mcp_ro", TLS: TLSVerify, Port: 14330, Production: true}
	archive := testDatabase("archive")
	shared, err := Export([]Database{billing, archive})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(shared, "airlockdb1:") || strings.ContainsAny(shared, " \n{}\"") {
		t.Fatalf("shared = %q, want one opaque airlockdb1: string", shared)
	}

	plan, err := PlanImport(nil, shared)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 || !plan.HasChanges() {
		t.Fatalf("changes = %+v, want the two exported databases", plan.Changes)
	}
	if plan.Changes[0].Database != archive || plan.Changes[0].Kind != ChangeAdded || plan.Changes[1].Database != billing {
		t.Fatalf("changes = %+v, want archive then legacy-billing, added", plan.Changes)
	}

	store, _ := newTestStore(t)
	if err = ApplyImport(store, &plan); err != nil {
		t.Fatal(err)
	}
	dbs, err := store.List()
	if err != nil || !reflect.DeepEqual(dbs, []Database{archive, billing}) {
		t.Fatalf("imported = %+v, %v", dbs, err)
	}
}

func TestExportHoldsOnlyTheDefinitions(t *testing.T) {
	shared, err := Export([]Database{testDatabase("billing")})
	if err != nil {
		t.Fatal(err)
	}
	data, err := sharecode.Decode("airlockdb1:", shared)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"version":   float64(1),
		"databases": map[string]any{"billing": map[string]any{"engine": "postgres", "host": "db.internal", "user": "app"}},
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %v, want %v", payload, want)
	}
}

func TestExportNeedsADatabase(t *testing.T) {
	if _, err := Export(nil); err == nil || !strings.Contains(err.Error(), "nothing to export") {
		t.Fatalf("err = %v, want nothing to export", err)
	}
}

func TestExportRefusesAnInvalidDatabase(t *testing.T) {
	db := testDatabase("billing")
	db.Host = ""
	if _, err := Export([]Database{db}); err == nil || !strings.Contains(err.Error(), `database "billing"`) || !strings.Contains(err.Error(), "host") {
		t.Fatalf("err = %v, want the invalid database and field named", err)
	}
}

func TestPlanImportListsTheChangedFields(t *testing.T) {
	local := testDatabase("billing")
	incoming := local
	incoming.Host, incoming.Port, incoming.Production = "db2.internal", 6432, true
	shared, err := Export([]Database{incoming})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanImport([]Database{local}, shared)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Kind != ChangeUpdated || plan.Changes[0].Database != incoming {
		t.Fatalf("changes = %+v, want billing changed", plan.Changes)
	}
	want := []FieldChange{
		{Field: "host", Old: "db.internal", New: "db2.internal"},
		{Field: "port", Old: "-", New: "6432"},
		{Field: "production", Old: "false", New: "true"},
	}
	if !reflect.DeepEqual(plan.Changes[0].Fields, want) {
		t.Fatalf("fields = %+v, want %+v", plan.Changes[0].Fields, want)
	}
}

func TestPlanImportWithNothingToChange(t *testing.T) {
	shared, err := Export([]Database{testDatabase("billing")})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanImport([]Database{testDatabase("billing")}, shared)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasChanges() || len(plan.Changes) != 1 || plan.Changes[0].Kind != ChangeUnchanged || plan.Changes[0].Fields != nil {
		t.Fatalf("changes = %+v, want billing unchanged", plan.Changes)
	}
}

func TestApplyImportNeverRemovesALocalDatabase(t *testing.T) {
	store, _ := newTestStore(t)
	for _, name := range []string{"billing", "local-only"} {
		if err := store.Put("", testDatabase(name)); err != nil {
			t.Fatal(err)
		}
	}
	incoming := testDatabase("billing")
	incoming.Host = "db2.internal"
	shared, err := Export([]Database{incoming, testDatabase("archive")})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanImport(current, shared)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyImport(store, &plan); err != nil {
		t.Fatal(err)
	}
	dbs, err := store.List()
	if err != nil || len(dbs) != 3 {
		t.Fatalf("List() = %+v, %v; want archive, billing and local-only", dbs, err)
	}
	got, _, err := store.Get("billing")
	if err != nil || got.Host != "db2.internal" {
		t.Fatalf("billing = %+v, %v; want the imported host", got, err)
	}
}

func TestImportRejectsDamagedStrings(t *testing.T) {
	shared, err := Export([]Database{testDatabase("billing")})
	if err != nil {
		t.Fatal(err)
	}
	altered := []byte(shared)
	last := len(altered) - 1
	if altered[last] == 'A' {
		altered[last] = 'B'
	} else {
		altered[last] = 'A'
	}
	for name, s := range map[string]string{
		"truncated":     shared[:len(shared)-4],
		"altered":       string(altered),
		"config string": "airlock1:" + strings.TrimPrefix(shared, "airlockdb1:"),
		"not base64":    "airlockdb1:!!!",
		"empty":         "airlockdb1:",
		"no database":   sharecode.Encode("airlockdb1:", []byte(`{"version": 1, "databases": {}}`)),
		"not json":      sharecode.Encode("airlockdb1:", []byte(`{`)),
		"trailing data": sharecode.Encode("airlockdb1:", []byte(`{"version":1,"databases":{"billing":{"engine":"postgres","host":"h","user":"u"}}} garbage`)),
	} {
		_, err := PlanImport(nil, s)
		if !errors.Is(err, ErrInvalidShare) {
			t.Fatalf("%s: err = %v, want ErrInvalidShare", name, err)
		}
		if err.Error() != "not a valid airlock database string (damaged or incomplete)" {
			t.Fatalf("%s: message = %q", name, err)
		}
	}
	if _, err := PlanImport(nil, "  "+shared+"\n"); err != nil {
		t.Fatalf("surrounding spaces from a paste must be accepted: %v", err)
	}
}

func TestImportRejectsUnknownFieldsVersionsAndInvalidDatabases(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		want    []string
	}{
		"unknown field":    {`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h", "user": "u", "password": "p"}}}`, []string{"password", "update airlock"}},
		"newer version":    {`{"version": 2, "databases": {"billing": {"engine": "postgres", "host": "h", "user": "u"}}}`, []string{"version 2", "update airlock"}},
		"invalid database": {`{"version": 1, "databases": {"archive": {"engine": "postgres", "host": "h", "user": "u"}, "billing": {"engine": "mysql", "host": "h", "user": "u"}}}`, []string{`database "billing"`, "engine"}},
	} {
		_, err := PlanImport(nil, sharecode.Encode("airlockdb1:", []byte(tc.payload)))
		if err == nil || errors.Is(err, ErrInvalidShare) {
			t.Fatalf("%s: err = %v, want a specific error", name, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: err = %v, want it to mention %q", name, err, want)
			}
		}
	}
}

func TestPlanImportRefusesControlCharactersAndJDBCSeparators(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		field   string
	}{
		"user hiding the plan": {`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h", "user": "ro\u001b[1A\u001b[2K\r"}}}`, "user"},
		"user C1 escape":       {`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h", "user": "ro\u009b2K"}}}`, "user"},
		"database newline":     {`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h", "database": "a\nb", "user": "u"}}}`, "database"},
		"database properties":  {`{"version": 1, "databases": {"billing": {"engine": "sqlserver", "host": "h", "database": "b;encrypt=false", "user": "u"}}}`, "database"},
		"host C1 escape":       {`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h\u009b", "user": "u"}}}`, "host"},
	} {
		_, err := PlanImport([]Database{testDatabase("billing")}, sharecode.Encode("airlockdb1:", []byte(tc.payload)))
		fieldErr, ok := errors.AsType[*FieldError](err)
		if !ok || fieldErr.Field != tc.field {
			t.Fatalf("%s: err = %v, want a %s field error", name, err, tc.field)
		}
		if !strings.Contains(err.Error(), `database "billing"`) {
			t.Fatalf("%s: err = %v, want it to name the database", name, err)
		}
	}
}

func TestImportChangeConnectionChanged(t *testing.T) {
	local := []Database{testDatabase("billing"), testDatabase("flagged"), testDatabase("same")}
	moved, flagged := local[0], local[1]
	moved.Host, moved.TLS = "evil.example", TLSDisable
	flagged.Production = true
	shared, err := Export([]Database{moved, flagged, local[2], testDatabase("added")})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanImport(local, shared)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for i := range plan.Changes {
		got[plan.Changes[i].Database.Name] = plan.Changes[i].ConnectionChanged()
	}
	want := map[string]bool{"added": false, "billing": true, "flagged": false, "same": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("connection changed = %v, want %v", got, want)
	}
}
