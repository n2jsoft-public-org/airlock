package dbmcp

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

func TestParameterNames(t *testing.T) {
	for declaration, want := range map[string][]string{
		"":                                nil,
		"@p0 int":                         {"@p0"},
		"@p0 int,@p1 int,@p2 nvarchar(6)": {"@p0", "@p1", "@p2"},
		" @Amount decimal(10, 2) , @n NVARCHAR(MAX)": {"@Amount", "@n"},
	} {
		got, err := parameterNames(declaration)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("parameterNames(%q) = %q, %v; want %q", declaration, got, err, want)
		}
	}
}

func TestParameterNamesRejectsAnythingButNamesAndTypes(t *testing.T) {
	for _, declaration := range []string{
		"p0 int",
		"@p0",
		"@p0 int OUTPUT",
		"@p0 int = 1",
		"@p0 int, ",
		"@p0 int) drop table users --",
		"@p0 int; drop table users",
		"@p0 nvarchar(6",
	} {
		if _, err := parameterNames(declaration); err == nil {
			t.Errorf("parameterNames(%q) accepted", declaration)
		}
	}
}

func TestPlanBatchWithoutParametersRunsTheStatement(t *testing.T) {
	batch, args, err := planBatch("select 1", "", nil, PlanEstimated)
	if err != nil || batch != "select 1" || args != nil {
		t.Fatalf("planBatch = %q, %v, %v", batch, args, err)
	}
}

func TestPlanBatchBindsStatementDeclarationAndValues(t *testing.T) {
	statement := "select TOP 1 ID from Items where ParentID = @p0 and Deleted is null and Code = @p2"
	batch, args, err := planBatch(statement, "@p0 int, @p2 nvarchar(6)", []any{float64(498554), "26003"}, PlanActual)
	if err != nil {
		t.Fatal(err)
	}
	if want := "EXEC sp_executesql @airlock_stmt, @airlock_params, @p0 = @airlock_v0, @p2 = @airlock_v1"; batch != want {
		t.Fatalf("batch = %q, want %q", batch, want)
	}
	want := []any{
		sql.Named("airlock_stmt", statement), sql.Named("airlock_params", "@p0 int, @p2 nvarchar(6)"),
		sql.Named("airlock_v0", "498554"), sql.Named("airlock_v1", "26003"),
	}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestPlanBatchRejectsMismatchedValuesAndEstimatedParameters(t *testing.T) {
	if _, _, err := planBatch("select 1", "@p0 int", nil, PlanActual); err == nil || !strings.Contains(err.Error(), "1 parameter(s) but 0 value(s)") {
		t.Errorf("missing value: %v", err)
	}
	if _, _, err := planBatch("select 1", "@p0 int", []any{1.0}, PlanEstimated); err == nil || !strings.Contains(err.Error(), "actual mode") {
		t.Errorf("estimated with parameters: %v", err)
	}
	if _, _, err := planBatch("select 1", "@p0 int", []any{map[string]any{}}, PlanActual); err == nil {
		t.Error("an object value must be rejected")
	}
}

func TestParameterValue(t *testing.T) {
	for _, tc := range []struct {
		in, want any
	}{
		{nil, nil}, {"abc", "abc"}, {float64(42), "42"}, {1.5, "1.5"}, {1e21, "1000000000000000000000"}, {true, "1"}, {false, "0"},
	} {
		if got, err := parameterValue(tc.in); err != nil || got != tc.want {
			t.Errorf("parameterValue(%v) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestPlanRejectedByGuardNeverReachesDatabase(t *testing.T) {
	q, db, opened := newTestQuerier(&sqlServerDialect, staticCreds{fallback: "app"})

	for _, req := range []PlanRequest{
		{SQL: "DELETE FROM users"},
		{SQL: "SELECT 1; DROP TABLE users"},
		{SQL: "SELECT 1", Mode: "profile"},
		{SQL: "SELECT @p0", Parameters: "@p0 int OUTPUT", Values: []any{1.0}},
		{SQL: "SELECT 1", IncludeJSON: true},
	} {
		if _, err := q.Plan(context.Background(), req); err == nil {
			t.Errorf("%+v: expected an error", req)
		}
	}
	if len(db.statements()) != 0 || len(*opened) != 0 {
		t.Fatalf("database was touched: %v %v", db.statements(), *opened)
	}
}

func TestPlanSettingsTurnCaptureOffInReverse(t *testing.T) {
	on, off := planSettings(PlanActual)
	if !slices.Equal(on, []string{"SET STATISTICS XML ON", "SET STATISTICS IO ON", "SET STATISTICS TIME ON"}) ||
		!slices.Equal(off, []string{"SET STATISTICS TIME OFF", "SET STATISTICS IO OFF", "SET STATISTICS XML OFF"}) {
		t.Fatalf("actual: on %q, off %q", on, off)
	}
	if on, off = planSettings(PlanEstimated); !slices.Equal(on, []string{"SET SHOWPLAN_XML ON"}) || !slices.Equal(off, []string{"SET SHOWPLAN_XML OFF"}) {
		t.Fatalf("estimated: on %q, off %q", on, off)
	}
}
