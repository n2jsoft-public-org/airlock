package dbmcp

import (
	"context"
	"database/sql/driver"
	"slices"
	"strings"
	"testing"
)

func readExplain(t *testing.T, name string) (plan StatementPlan, rowCount int64) {
	t.Helper()
	plan, rowCount, err := summarizeExplain(readFile(t, "testdata/"+name))
	if err != nil {
		t.Fatal(err)
	}
	return plan, rowCount
}

// EXPLAIN (ANALYZE, BUFFERS, VERBOSE, SETTINGS, FORMAT JSON) of a parallel hash join sorted on
// disk, with a misestimated filter (PostgreSQL 17, work_mem 64kB, enable_nestloop off).
func TestSummarizeActualExplain(t *testing.T) {
	plan, rowCount := readExplain(t, "explain_actual.json")

	if rowCount != 30000 || plan.EstimatedRows != 88 || plan.EstimatedCost != 5196.91 {
		t.Fatalf("rows %d, estimated rows %v, cost %v", rowCount, plan.EstimatedRows, plan.EstimatedCost)
	}
	if plan.PlanningTimeMs == nil || *plan.PlanningTimeMs != 0.81 || plan.ExecutionTimeMs == nil || *plan.ExecutionTimeMs != 36.941 {
		t.Fatalf("planning %v, execution %v", plan.PlanningTimeMs, plan.ExecutionTimeMs)
	}
	if !slices.Equal(plan.Settings, []string{"enable_nestloop = off", "work_mem = 64kB"}) {
		t.Fatalf("settings = %q", plan.Settings)
	}
	if want := []string{
		"Gather Merge | cost 5186.79..5196.91 | est rows 88 | actual rows 30000 x 1 loops | time 28.17..35.982 ms | workers 1 launched of 1 | buffers hit 2033 read 0 | temp read 234 written 261",
		"  Sort | cost 4186.78..4187 | est rows 88 | actual rows 15000 x 2 loops | time 25.738..27.368 ms | buffers hit 2033 read 0 | temp read 234 written 261 | sort key o.amount DESC | sort external merge, Disk 456 kB",
		"    Hash Join | cost 44.01..4183.93 | est rows 88 | actual rows 15000 x 2 loops | time 0.264..18.896 ms | buffers hit 1977 read 0 | hash cond (o.customer_id = c.id)",
		"      Parallel Seq Scan on public.orders o | cost 0..3675.71 | est rows 176471 | actual rows 150000 x 2 loops | time 0.01..8.665 ms | buffers hit 1911 read 0",
		"      Hash | cost 44..44 | est rows 1 | actual rows 200 x 2 loops | time 0.22..0.221 ms | buffers hit 18 read 0",
		"        Seq Scan on public.customers c | cost 0..44 | est rows 1 | actual rows 200 x 2 loops | time 0.017..0.193 ms | buffers hit 18 read 0 | filter ((c.country = 'FR'::text) AND ((c.id % 10) = 0)) removed 1800 rows",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s\nwant:\n%s", strings.Join(plan.Operators, "\n"), strings.Join(want, "\n"))
	}
	// The misestimate starts at the customers scan: the nodes above only inherit it.
	if want := []string{
		"Sort spilled 456 kB to disk: raise work_mem or sort fewer rows",
		"Parallel Seq Scan on public.orders o read 300000 rows to return 300000",
		"Seq Scan on public.customers c: estimated 1 rows, got 200 per loop; stale statistics (ANALYZE) or correlated predicates mislead the planner, and the nodes above inherit the error",
	}; !slices.Equal(plan.Warnings, want) {
		t.Fatalf("warnings:\n%s", strings.Join(plan.Warnings, "\n"))
	}
}

// EXPLAIN (VERBOSE, FORMAT JSON) of the same statement.
func TestSummarizeEstimatedExplain(t *testing.T) {
	plan, rowCount := readExplain(t, "explain_estimated.json")

	if rowCount != 0 || plan.PlanningTimeMs != nil || plan.ExecutionTimeMs != nil || plan.Settings != nil {
		t.Fatalf("an estimated plan has no measures: %+v, rows %d", plan, rowCount)
	}
	if got := plan.Operators[0]; got != "Gather Merge | cost 5186.79..5196.91 | est rows 88 | workers 1 planned" {
		t.Fatalf("root = %q", got)
	}
	if !slices.Equal(plan.Warnings, []string{"Parallel Seq Scan on public.orders o is estimated to return 176471 rows"}) {
		t.Fatalf("warnings = %q", plan.Warnings)
	}
}

func TestSummarizeExplainSpilledHashAndNodesNeverExecuted(t *testing.T) {
	plan, _, err := summarizeExplain(`[{"Plan": {"Node Type": "Hash Join", "Join Type": "Left", "Total Cost": 10, "Plan Rows": 5,
		"Actual Rows": 5, "Actual Loops": 1, "Join Filter": "(a.x > b.y)", "Rows Removed by Join Filter": 7, "Plans": [
		{"Node Type": "Index Only Scan", "Index Name": "a_pkey", "Relation Name": "a", "Schema": "public", "Alias": "a",
		 "Index Cond": "(id = 1)", "Plan Rows": 1, "Actual Rows": 0, "Actual Loops": 0},
		{"Node Type": "Hash", "Hash Batches": 8, "Plan Rows": 1, "Actual Rows": 1, "Actual Loops": 1}]}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"Hash Join (Left) | cost 0..10 | est rows 5 | actual rows 5 x 1 loops | time 0..0 ms | join filter (a.x > b.y) removed 7 rows",
		"  Index Only Scan using a_pkey on public.a | cost 0..0 | est rows 1 | never executed | index cond (id = 1)",
		"  Hash | cost 0..0 | est rows 1 | actual rows 1 x 1 loops | time 0..0 ms | hash batches 8",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s", strings.Join(plan.Operators, "\n"))
	}
	if !slices.Equal(plan.Warnings, []string{"Hash spilled to disk in 8 batches: raise work_mem or hash fewer rows"}) {
		t.Fatalf("warnings = %q", plan.Warnings)
	}
}

func TestPostgresPlanRunsExplainInRolledBackReadOnlyTransaction(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	document := readFile(t, "testdata/explain_actual.json")
	db.answer("EXPLAIN", []string{"QUERY PLAN"}, []driver.Value{document})

	result, err := q.Plan(context.Background(), PlanRequest{
		SQL: "SELECT * FROM orders WHERE customer_id = $1 AND status = $2", Values: []any{42.0, "open"}, IncludeJSON: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"BEGIN READ ONLY", "SET LOCAL standard_conforming_strings = on", "SET LOCAL statement_timeout = '30s'",
		"EXPLAIN (ANALYZE, BUFFERS, VERBOSE, SETTINGS, FORMAT JSON) SELECT * FROM orders WHERE customer_id = $1 AND status = $2 | 42 | open",
		"ROLLBACK",
	}
	if got := db.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements:\n%q\nwant:\n%q", got, want)
	}
	if result.Mode != PlanActual || result.RowCount == nil || *result.RowCount != 30000 || string(result.PlanJSON) != document {
		t.Fatalf("result = %+v", result)
	}
	if s := result.Statements; len(s) != 1 || s[0].Statement != "SELECT * FROM orders WHERE customer_id = $1 AND status = $2" {
		t.Fatalf("statements = %+v", s)
	}
}

func TestPostgresEstimatedPlanSkipsAnalyze(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answer("EXPLAIN", []string{"QUERY PLAN"}, []driver.Value{readFile(t, "testdata/explain_estimated.json")})

	result, err := q.Plan(context.Background(), PlanRequest{SQL: "SELECT 1", Mode: PlanEstimated})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(db.statements(), "EXPLAIN (VERBOSE, FORMAT JSON) SELECT 1") || result.RowCount != nil || result.PlanJSON != nil {
		t.Fatalf("statements %q, result %+v", db.statements(), result)
	}
}

func TestPostgresPlanRejectsSQLServerOptionsBeforeTheDatabase(t *testing.T) {
	q, db, opened := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})

	for _, req := range []PlanRequest{
		{SQL: "DELETE FROM users"},
		{SQL: "SELECT 1; DROP TABLE users"},
		{SQL: "SELECT $1", Parameters: "@p0 int", Values: []any{1.0}},
		{SQL: "SELECT 1", IncludeXML: true},
		{SQL: "SELECT $1", Values: []any{map[string]any{}}},
	} {
		if _, err := q.Plan(context.Background(), req); err == nil {
			t.Errorf("%+v: expected an error", req)
		}
	}
	if len(db.statements()) != 0 || len(*opened) != 0 {
		t.Fatalf("database was touched: %v %v", db.statements(), *opened)
	}
}
