package directdb

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/sharecode"
)

func TestExportImportRoundTrip(t *testing.T) {
	billing := Database{Name: "legacy-billing", Engine: EngineSQLServer, Host: "billing.db.internal", User: "mcp_ro", TLS: TLSVerify, Port: 14330, Environment: "prod"}
	archive := testDatabase("archive")
	shop := Database{Name: "shop", Engine: EngineMySQL, Host: "shop.db.internal", User: "reader"}
	wiki := Database{Name: "wiki", Engine: EngineMariaDB, Host: "127.0.0.1", User: "root", TLS: TLSDisable, Database: "wiki"}
	shared, err := Export([]Database{billing, archive, shop, wiki})
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
	if len(plan.Changes) != 4 || !plan.HasChanges() {
		t.Fatalf("changes = %+v, want the four exported databases", plan.Changes)
	}
	if plan.Changes[0].Database != archive || plan.Changes[0].Kind != ChangeAdded || plan.Changes[1].Database != billing {
		t.Fatalf("changes = %+v, want archive then legacy-billing, added", plan.Changes)
	}

	writer := &memoryWriter{}
	if err = ApplyImport(writer, &plan); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || !reflect.DeepEqual(writer.written, []Database{archive, billing, shop, wiki}) {
		t.Fatalf("written = %+v in %d calls, want both databases in one write", writer.written, writer.calls)
	}
}

// memoryWriter records what ApplyImport writes.
type memoryWriter struct {
	written []Database
	calls   int
}

func (w *memoryWriter) PutAll(dbs []Database) error {
	w.calls++
	w.written = append(w.written, dbs...)
	return nil
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
	incoming.Host, incoming.Port, incoming.Environment = "db2.internal", 6432, "prod"
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
		{Field: "environment", Old: "-", New: "prod"},
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

func TestApplyImportWritesOnlyTheAddedAndChangedDatabases(t *testing.T) {
	kept, changed := testDatabase("archive"), testDatabase("billing")
	changed.Port = 6432
	plan := ImportPlan{Changes: []ImportChange{
		{Kind: ChangeUnchanged, Database: kept},
		{Kind: ChangeUpdated, Database: changed},
		{Kind: ChangeAdded, Database: testDatabase("events")},
	}}
	writer := &memoryWriter{}
	if err := ApplyImport(writer, &plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.written, []Database{changed, testDatabase("events")}) {
		t.Fatalf("written = %+v, want billing and events only", writer.written)
	}
	if err := ApplyImport(writer, &ImportPlan{Changes: plan.Changes[:1]}); err != nil || writer.calls != 1 {
		t.Fatalf("a plan without change must write nothing: calls = %d, err = %v", writer.calls, err)
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
		"invalid database": {`{"version": 1, "databases": {"archive": {"engine": "postgres", "host": "h", "user": "u"}, "billing": {"engine": "oracle", "host": "h", "user": "u"}}}`, []string{`database "billing"`, "engine"}},
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
	flagged.Environment = "prod"
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

func TestImportOfALegacyProductionDatabaseIsProduction(t *testing.T) {
	legacy := sharecode.Encode(sharePrefix, []byte(`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h", "user": "u", "production": true}}}`))
	plan, err := PlanImport(nil, legacy)
	if err != nil {
		t.Fatal(err)
	}
	live, err := environment.Parse("live!=*-live")
	if err != nil {
		t.Fatal(err)
	}
	if db := plan.Changes[0].Database; db.Environment != "prod" || !live.Lookup(db.Environment).Production {
		t.Fatalf("Environment = %q: a production database must stay production where production is named live", db.Environment)
	}
}
