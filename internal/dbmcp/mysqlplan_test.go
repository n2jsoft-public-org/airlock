package dbmcp

import (
	"context"
	"database/sql/driver"
	"slices"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// The fixtures plan, on MySQL 8.4 and MariaDB 11.4, a join forced to scan 150000 orders whose
// amount filter keeps 1350 (estimated 50054 on MySQL, all of them on MariaDB):
// SELECT STRAIGHT_JOIN c.country, SUM(o.amount) AS total FROM orders o JOIN customers c ON c.id = o.customer_id
// WHERE o.amount > 990 AND c.name LIKE 'c%' GROUP BY c.country ORDER BY total DESC

// EXPLAIN ANALYZE (text tree) of MySQL 8.4.
func TestSummarizeMySQLTree(t *testing.T) {
	plan, rowCount := summarizeMySQLTree(readFile(t, "testdata/mysql_actual.txt"))

	if rowCount == nil || *rowCount != 1 || plan.EstimatedCost != 32641 || plan.EstimatedRows != 5561 {
		t.Fatalf("rows %v, cost %v, estimated rows %v", rowCount, plan.EstimatedCost, plan.EstimatedRows)
	}
	if plan.ExecutionTimeMs == nil || *plan.ExecutionTimeMs != 13.7 {
		t.Fatalf("execution %v", plan.ExecutionTimeMs)
	}
	if want := []string{
		"Sort: total DESC | actual rows 1 x 1 loops | time 13.7..13.7 ms",
		"  Table scan on <temporary> | actual rows 1 x 1 loops | time 13.7..13.7 ms",
		"    Aggregate using temporary table | actual rows 1 x 1 loops | time 13.7..13.7 ms",
		"      Nested loop inner join | cost 32641 | est rows 5561 | actual rows 1350 x 1 loops | time 0.487..13.5 ms",
		"        Filter: (o.amount > 990) | cost 15122 | est rows 50054 | actual rows 1350 x 1 loops | time 0.478..12.8 ms",
		"          Table scan on o | cost 15122 | est rows 150176 | actual rows 150000 x 1 loops | time 0.428..10.5 ms",
		"        Filter: (c.`name` like 'c%') | cost 0.25 | est rows 0.111 | actual rows 1 x 1350 loops | time 0.000383..0.000423 ms",
		"          Single-row index lookup on c using PRIMARY (id=o.customer_id) | cost 0.25 | est rows 1 | actual rows 1 x 1350 loops | time 0.000316..0.000329 ms",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s", strings.Join(plan.Operators, "\n"))
	}
	// The misestimate starts at the amount filter: the join above only inherits it.
	if want := []string{
		"Filter: (o.amount > 990): estimated 50054 rows, got 1350 per loop; stale statistics (ANALYZE TABLE) or correlated conditions mislead the optimizer, and the nodes above inherit the error",
		"Table scan on o read 150000 rows: an index matching the filter may help",
	}; !slices.Equal(plan.Warnings, want) {
		t.Fatalf("warnings:\n%s", strings.Join(plan.Warnings, "\n"))
	}
}

func TestSummarizeMySQLTreeNeverExecuted(t *testing.T) {
	plan, _ := summarizeMySQLTree("-> Nested loop inner join  (cost=1.1 rows=1) (actual time=0.01..0.01 rows=0 loops=1)\n" +
		"    -> Table scan on a  (cost=0.35 rows=1) (actual time=0.009..0.009 rows=0 loops=1)\n" +
		"    -> Index lookup on b using a_id (a_id=a.id)  (cost=0.35 rows=1) (never executed)\n")
	if got := plan.Operators[2]; got != "  Index lookup on b using a_id (a_id=a.id) | cost 0.35 | est rows 1 | never executed" {
		t.Fatalf("operator = %q", got)
	}
}

// EXPLAIN ANALYZE FORMAT=JSON (version 2) of MySQL 8.4: the nodes of the text tree.
func TestSummarizeMySQLJSONTree(t *testing.T) {
	nodes, err := mysqlJSONTree(readFile(t, "testdata/mysql_actual.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, rowCount := summarizeTree(nodes)

	if rowCount == nil || *rowCount != 1 || plan.EstimatedCost != 32570 || plan.EstimatedRows != 5549 {
		t.Fatalf("rows %v, cost %v, estimated rows %v", rowCount, plan.EstimatedCost, plan.EstimatedRows)
	}
	if plan.ExecutionTimeMs == nil || *plan.ExecutionTimeMs != 19.6 {
		t.Fatalf("execution %v", plan.ExecutionTimeMs)
	}
	if want := []string{
		"Sort: total DESC | actual rows 1 x 1 loops | time 19.6..19.6 ms",
		"  Table scan on <temporary> | actual rows 1 x 1 loops | time 19.6..19.6 ms",
		"    Aggregate using temporary table | actual rows 1 x 1 loops | time 19.6..19.6 ms",
		"      Nested loop inner join | cost 32570 | est rows 5549 | actual rows 75 x 1 loops | time 0.572..19.5 ms",
		"        Filter: ((o.amount > 990.00) and (o.customer_id is not null)) | cost 15089 | est rows 49945 | actual rows 1350 x 1 loops | time 0.454..18.5 ms",
		"          Table scan on o | cost 15089 | est rows 149850 | actual rows 150000 x 1 loops | time 0.434..12.6 ms",
		"        Filter: (c.`name` like 'c%') | cost 0.25 | est rows 0.111 | actual rows 0.0556 x 1350 loops | time 0.00064..0.000642 ms",
		"          Single-row index lookup on c using PRIMARY (id=o.customer_id) | cost 0.25 | est rows 1 | actual rows 1 x 1350 loops | time 0.000505..0.000521 ms",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s", strings.Join(plan.Operators, "\n"))
	}
	if want := []string{
		"Filter: ((o.amount > 990.00) and (o.customer_id is not null)): estimated 49945 rows, got 1350 per loop; stale statistics (ANALYZE TABLE) or correlated conditions mislead the optimizer, and the nodes above inherit the error",
		"Table scan on o read 150000 rows: an index matching the filter may help",
	}; !slices.Equal(plan.Warnings, want) {
		t.Fatalf("warnings:\n%s", strings.Join(plan.Warnings, "\n"))
	}
}

// Newer versions put the plan under query_plan; a node never run has no actual values.
func TestMySQLJSONTreeQueryPlanAndNeverExecuted(t *testing.T) {
	nodes, err := mysqlJSONTree(`{"query_plan": {"operation": "Nested loop inner join", "estimated_rows": 1.0,
"estimated_total_cost": 0.7, "actual_rows": 0.0, "actual_loops": 1, "actual_first_row_ms": 0.010208, "actual_last_row_ms": 0.010208,
"inputs": [{"operation": "Covering index scan on e using PRIMARY", "estimated_rows": 1.0, "estimated_total_cost": 0.35,
"actual_rows": 0.0, "actual_loops": 1, "actual_first_row_ms": 0.007458, "actual_last_row_ms": 0.007458},
{"operation": "Single-row covering index lookup on o using PRIMARY (id=e.id)", "estimated_rows": 1.0, "estimated_total_cost": 0.35}]},
"json_schema_version": "2.0"}`)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := summarizeTree(nodes)
	if want := []string{
		"Nested loop inner join | cost 0.7 | est rows 1 | actual rows 0 x 1 loops | time 0.0102..0.0102 ms",
		"  Covering index scan on e using PRIMARY | cost 0.35 | est rows 1 | actual rows 0 x 1 loops | time 0.00746..0.00746 ms",
		"  Single-row covering index lookup on o using PRIMARY (id=e.id) | cost 0.35 | est rows 1 | never executed",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s", strings.Join(plan.Operators, "\n"))
	}
	if _, err := mysqlJSONTree(`{"query_block": {}}`); err == nil {
		t.Fatal("a version 1 plan must be refused")
	}
}

// EXPLAIN FORMAT=JSON of MySQL 8.4.
func TestSummarizeMySQLEstimatedJSON(t *testing.T) {
	plan, err := summarizeMySQLJSON(readFile(t, "testdata/mysql_estimated.json"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.EstimatedCost != 32640.63 || plan.ExecutionTimeMs != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if want := []string{
		"Select #1 | cost 32640.63",
		"  Sort | using filesort",
		"    Group | using temporary table",
		"      Nested loop join",
		"        Table o | access ALL | cost 15121.85 | est rows 150176 | filtered 33.33% | filter (`shop`.`o`.`amount` > 990)",
		"        Table c | access eq_ref | key PRIMARY | cost 32640.63 | est rows 1 | filtered 11.11% | filter (`shop`.`c`.`name` like 'c%')",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s", strings.Join(plan.Operators, "\n"))
	}
	if want := []string{"Table o (full table scan) is estimated to read 150176 rows per scan: an index matching the filter may help"}; !slices.Equal(plan.Warnings, want) {
		t.Fatalf("warnings = %q", plan.Warnings)
	}
}

// ANALYZE FORMAT=JSON of MariaDB 11.4.
func TestSummarizeMariaDBActualJSON(t *testing.T) {
	plan, err := summarizeMySQLJSON(readFile(t, "testdata/mariadb_actual.json"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.EstimatedCost != 159.17 || plan.PlanningTimeMs == nil || plan.ExecutionTimeMs == nil || *plan.ExecutionTimeMs != 10.916 {
		t.Fatalf("plan = %+v", plan)
	}
	if want := []string{
		"Select #1 | cost 159.17",
		"  Filesort | sort key sum(o.amount) desc | actual rows 1",
		"    Temporary table",
		"      Nested loop join",
		"        Table o | access ALL | cost 24.85 | est rows 150176 | filtered 100% | actual rows 150000 x 1 loops | actual filtered 0.9% | time 10.266 ms | filter o.amount > 990",
		"        Table c | access eq_ref | key PRIMARY | cost 134.32 | est rows 1 | filtered 100% | actual rows 1 x 1350 loops | actual filtered 100% | time 0.627 ms | filter c.`name` like 'c%'",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s", strings.Join(plan.Operators, "\n"))
	}
	if want := []string{
		"Table o: estimated 150176 rows, got 1350 per loop; stale statistics (ANALYZE TABLE) or correlated conditions mislead the optimizer",
		"Table o (full table scan) read 150000 rows: an index matching the filter may help",
	}; !slices.Equal(plan.Warnings, want) {
		t.Fatalf("warnings:\n%s", strings.Join(plan.Warnings, "\n"))
	}
}

func TestSummarizeMySQLJSONSubqueriesAfterTables(t *testing.T) {
	plan, err := summarizeMySQLJSON(`{"query_block": {"select_id": 1, "message": "No tables used",
		"select_list_subqueries": [{"dependent": false, "query_block": {"select_id": 2,
			"table": {"table_name": "t", "access_type": "index", "key": "k", "using_index": true, "rows_examined_per_scan": 3}}}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"Select #1 | No tables used",
		"  Select #2",
		"    Table t | access index | key k | covering index | est rows 3",
	}; !slices.Equal(plan.Operators, want) {
		t.Fatalf("operators:\n%s", strings.Join(plan.Operators, "\n"))
	}
}

func TestMySQLPlanRunsInRolledBackReadOnlyTransaction(t *testing.T) {
	const args = "SELECT * FROM orders WHERE customer_id = ? AND status = ? | 42 | open"
	for _, tc := range []struct {
		dialect    *dialect
		mode       string
		fixture    string
		statements []string
		oldVersion bool // MySQL before 8.3, which has no explain_json_format_version
	}{
		{&mysqlDialect, PlanActual, "mysql_actual.json", []string{
			"BEGIN READ ONLY", "SET SESSION explain_json_format_version = 2", "EXPLAIN ANALYZE FORMAT=JSON " + args, "ROLLBACK",
		}, false},
		{&mysqlDialect, PlanActual, "mysql_actual.txt", []string{
			"BEGIN READ ONLY", "SET SESSION explain_json_format_version = 2", "ROLLBACK",
			"BEGIN READ ONLY", "EXPLAIN ANALYZE " + args, "ROLLBACK",
		}, true},
		{&mysqlDialect, PlanEstimated, "mysql_estimated.json", []string{
			"BEGIN READ ONLY", "SET SESSION explain_json_format_version = 1", "EXPLAIN FORMAT=JSON " + args, "ROLLBACK",
		}, false},
		{&mysqlDialect, PlanEstimated, "mysql_estimated.json", []string{
			"BEGIN READ ONLY", "SET SESSION explain_json_format_version = 1", "ROLLBACK",
			"BEGIN READ ONLY", "EXPLAIN FORMAT=JSON " + args, "ROLLBACK",
		}, true},
		{&mariaDBDialect, PlanActual, "mariadb_actual.json", []string{"BEGIN READ ONLY", "ANALYZE FORMAT=JSON " + args, "ROLLBACK"}, false},
	} {
		q, db, _ := newTestQuerier(tc.dialect, staticCreds{fallback: "shop"})
		// The plan answers its statement only: the fake picks any answer whose prefix matches.
		explain := strings.Fields(tc.statements[len(tc.statements)-2])[0]
		db.answer(explain, []string{"EXPLAIN"}, []driver.Value{readFile(t, "testdata/"+tc.fixture)})
		if tc.oldVersion {
			db.answers["SET SESSION explain_json_format_version"] = fakeAnswer{err: &mysql.MySQLError{Number: mysqlUnknownVariable}}
		}

		result, err := q.Plan(context.Background(), &PlanRequest{
			SQL: "SELECT * FROM orders WHERE customer_id = ? AND status = ?", Values: []any{42.0, "open"}, Mode: tc.mode,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := db.statements(), tc.statements; !slices.Equal(got, want) {
			t.Fatalf("statements:\n%q\nwant:\n%q", got, want)
		}
		if result.Mode != tc.mode || len(result.Statements) != 1 || len(result.Statements[0].Operators) == 0 ||
			result.Statements[0].Statement != "SELECT * FROM orders WHERE customer_id = ? AND status = ?" {
			t.Fatalf("result = %+v", result)
		}
	}
}

func TestMySQLPlanExplainsOldVersionsAndRejectsOtherEnginesOptions(t *testing.T) {
	q, db, opened := newTestQuerier(&mysqlDialect, staticCreds{fallback: "shop"})
	for _, req := range []PlanRequest{
		{SQL: "DELETE FROM users"},
		{SQL: "SELECT ?", Parameters: "@p0 int", Values: []any{1.0}},
		{SQL: "SELECT 1", IncludeXML: true},
	} {
		if _, err := q.Plan(context.Background(), &req); err == nil {
			t.Errorf("%+v: expected an error", req)
		}
	}
	if len(db.statements()) != 0 || len(*opened) != 0 {
		t.Fatalf("database was touched: %v %v", db.statements(), *opened)
	}

	// Before 8.3, EXPLAIN ANALYZE has no JSON output; before 8.0.18, there is no EXPLAIN ANALYZE.
	db.answers["SET SESSION explain_json_format_version"] = fakeAnswer{err: &mysql.MySQLError{Number: mysqlUnknownVariable}}
	if _, err := q.Plan(context.Background(), &PlanRequest{SQL: "SELECT 1", IncludeJSON: true}); err == nil || !strings.Contains(err.Error(), "8.3") {
		t.Fatalf("err = %v, want the version needed", err)
	}
	if got := db.statements(); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "EXPLAIN") }) {
		t.Fatalf("the statement was explained anyway: %q", got)
	}
	db.answers["EXPLAIN ANALYZE"] = fakeAnswer{err: &mysql.MySQLError{Number: mysqlParseError, Message: "You have an error in your SQL syntax"}}
	if _, err := q.Plan(context.Background(), &PlanRequest{SQL: "SELECT 1"}); err == nil || !strings.Contains(err.Error(), "8.0.18") {
		t.Fatalf("err = %v, want the version needed", err)
	}
}
