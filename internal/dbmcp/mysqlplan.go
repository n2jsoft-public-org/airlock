package dbmcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// MySQL errors: a statement it cannot parse (ER_PARSE_ERROR), a variable it does not know
// (ER_UNKNOWN_SYSTEM_VARIABLE).
const (
	mysqlParseError      = 1064
	mysqlUnknownVariable = 1193
)

// mysqlExplainStatement returns the statement giving the plan of statement in mode: JSON plans,
// but for MySQL's EXPLAIN ANALYZE before MySQL 8.3 (jsonVersion false), which only has a text tree.
func mysqlExplainStatement(engine Engine, mode string, jsonVersion bool, statement string) string {
	switch {
	case mode == PlanEstimated:
		return "EXPLAIN FORMAT=JSON " + statement
	case engine == EngineMariaDB:
		return "ANALYZE FORMAT=JSON " + statement
	case jsonVersion:
		return "EXPLAIN ANALYZE FORMAT=JSON " + statement
	default:
		return "EXPLAIN ANALYZE " + statement
	}
}

// mysqlJSONVersion picks the JSON plan format of MySQL 8.3 or later for mode: version 2 for
// EXPLAIN ANALYZE, which has no other, version 1 for EXPLAIN, whatever an earlier plan of the
// pooled connection chose. Older versions do not know the variable.
func mysqlJSONVersion(mode string) string {
	if mode == PlanActual {
		return "SET SESSION explain_json_format_version = 2"
	}
	return "SET SESSION explain_json_format_version = 1"
}

// mysqlPlan returns the plan of a statement checked read-only. Actual plans run it in a read-only
// transaction always rolled back, so even a statement slipping past the guard changes nothing.
func (q *sqlQuerier) mysqlPlan(ctx context.Context, req *PlanRequest, mode string) (PlanResult, error) {
	engine := q.dialect.engine
	switch {
	case strings.TrimSpace(req.Parameters) != "":
		return PlanResult{}, errors.New("MySQL takes no parameter declaration: use ? in the statement and pass their values")
	case req.IncludeXML:
		return PlanResult{}, errors.New("include_xml is for SQL Server: use include_json for the raw EXPLAIN FORMAT=JSON output")
	}
	args := make([]any, len(req.Values))
	for i, value := range req.Values {
		v, err := parameterValue(value)
		if err != nil {
			return PlanResult{}, fmt.Errorf("value of ? number %d: %w", i+1, err)
		}
		args[i] = v
	}

	var document string
	var jsonVersion bool // MySQL 8.3 or later, which chooses its JSON plan format
	err := q.withPool(ctx, req.Database, func(db *sql.DB) (err error) {
		if jsonVersion = engine == EngineMySQL; jsonVersion {
			document, err = q.explainFrom(ctx, db, mysqlExplainStatement(engine, mode, true, req.SQL), args, mysqlJSONVersion(mode))
			if !isMySQLError(err, mysqlUnknownVariable) {
				return err
			}
			jsonVersion = false
			if req.IncludeJSON && mode == PlanActual {
				return errors.New("MySQL's EXPLAIN ANALYZE has JSON output from MySQL 8.3: on older versions use the estimated mode with include_json")
			}
		}
		document, err = q.explainFrom(ctx, db, mysqlExplainStatement(engine, mode, false, req.SQL), args)
		return err
	})
	tree := mode == PlanActual && engine == EngineMySQL
	if isMySQLError(err, mysqlParseError) && tree && !jsonVersion {
		return PlanResult{}, fmt.Errorf("EXPLAIN ANALYZE needs MySQL 8.0.18 or later: on older versions use the estimated mode: %w", err)
	}
	if err != nil {
		return PlanResult{}, err
	}

	var statement StatementPlan
	var rowCount *int64
	switch {
	case tree && jsonVersion:
		var nodes []treeNode
		if nodes, err = mysqlJSONTree(document); err != nil {
			return PlanResult{}, fmt.Errorf("reading the plan: %w", err)
		}
		statement, rowCount = summarizeTree(nodes)
	case tree:
		statement, rowCount = summarizeMySQLTree(document)
	default:
		if statement, err = summarizeMySQLJSON(document); err != nil {
			return PlanResult{}, fmt.Errorf("reading the plan: %w", err)
		}
	}
	statement.Statement = strings.Join(strings.Fields(req.SQL), " ")
	result := PlanResult{Mode: mode, Statements: []StatementPlan{statement}, RowCount: rowCount}
	if req.IncludeJSON {
		result.PlanJSON = json.RawMessage(document)
	}
	return result, nil
}

// isMySQLError reports whether err is the MySQL error number.
func isMySQLError(err error, number uint16) bool {
	myErr, ok := errors.AsType[*mysql.MySQLError](err)
	return ok && myErr.Number == number
}

// rowsMisestimated reports whether estimated and actual rows are far apart enough to matter.
func rowsMisestimated(estimated, actual float64) bool {
	low, high := min(estimated, actual), max(estimated, actual)
	return high >= estimateMissRows && high >= estimateMissRatio*max(low, 1)
}

// roundTo rounds f to digits decimals, for display.
func roundTo(f float64, digits int) float64 {
	scale := math.Pow10(digits)
	return math.Round(f*scale) / scale
}

// planNumber reads a number of a JSON plan: MySQL writes some as strings ("filtered": "33.33").
func planNumber(value any) (float64, bool) {
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
	case string:
		text = v
	default:
		return 0, false
	}
	f, err := strconv.ParseFloat(text, 64)
	return f, err == nil
}

// planString is the text at key in object; "" when it is missing or not text.
func planString(object map[string]any, key string) string {
	if s, ok := object[key].(string); ok {
		return s
	}
	return ""
}

// mysqlPlanNumber is the number at path in object, e.g. "cost_info", "query_cost".
func mysqlPlanNumber(object map[string]any, path ...string) (float64, bool) {
	for _, key := range path[:len(path)-1] {
		child, ok := object[key].(map[string]any)
		if !ok {
			return 0, false
		}
		object = child
	}
	return planNumber(object[path[len(path)-1]])
}

// mysqlOperations names the operations of MySQL and MariaDB JSON plans shown as operators; other
// objects are walked through for the tables they hold.
var mysqlOperations = map[string]string{
	"nested_loop":                "Nested loop join",
	"ordering_operation":         "Sort",
	"grouping_operation":         "Group",
	"duplicates_removal":         "Distinct",
	"windowing":                  "Window",
	"union_result":               "Union",
	"materialized_from_subquery": "Materialized subquery",
	"filesort":                   "Filesort",
	"temporary_table":            "Temporary table",
	"read_sorted_file":           "Read sorted file",
	"block-nl-join":              "Block nested loop join",
	"materialized":               "Materialized subquery",
	"derived":                    "Derived table",
}

// summarizeMySQLJSON reads an EXPLAIN FORMAT=JSON (MySQL, MariaDB) or ANALYZE FORMAT=JSON
// (MariaDB) document of one statement.
func summarizeMySQLJSON(document string) (StatementPlan, error) {
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	var plan map[string]any
	if err := decoder.Decode(&plan); err != nil {
		return StatementPlan{}, err
	}
	block, ok := plan["query_block"].(map[string]any)
	if !ok {
		return StatementPlan{}, errors.New("no query_block in the plan")
	}
	s := StatementPlan{}
	if cost, ok := mysqlPlanNumber(block, "cost_info", "query_cost"); ok {
		s.EstimatedCost = cost
	} else if cost, ok := mysqlPlanNumber(block, "cost"); ok {
		s.EstimatedCost = roundTo(cost, 2)
	}
	if planning, ok := mysqlPlanNumber(plan, "query_optimization", "r_total_time_ms"); ok {
		s.PlanningTimeMs = new(roundTo(planning, 3))
	}
	if execution, ok := mysqlPlanNumber(block, "r_total_time_ms"); ok {
		s.ExecutionTimeMs = new(roundTo(execution, 3))
	}
	w := &mysqlPlanWalker{}
	w.walk("query_block", block, 0)
	s.Operators, s.Warnings = w.operators, w.warnings
	if s.Operators == nil {
		s.Operators = []string{}
	}
	return s, nil
}

// mysqlPlanWalker renders the operators and warnings of a MySQL or MariaDB JSON plan.
type mysqlPlanWalker struct {
	operators []string
	warnings  []string
}

// walk renders value, found under key: a table, a query block, an operation, or an object or
// array holding them.
func (w *mysqlPlanWalker) walk(key string, value any, level int) {
	switch v := value.(type) {
	case []any:
		if label := mysqlOperations[key]; label != "" {
			w.operators = append(w.operators, strings.Repeat("  ", level)+label)
			level++
		}
		for _, item := range v {
			w.walk("", item, level)
		}
	case map[string]any:
		switch {
		case key == "table":
			w.table(v, level)
			level++
		case key == "query_block":
			w.queryBlock(v, level)
			level++
		case mysqlOperations[key] != "":
			w.operation(mysqlOperations[key], v, level)
			level++
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// Subqueries come after the tables of their block, as in the text EXPLAIN.
		slices.SortFunc(keys, func(a, b string) int {
			if sa, sb := strings.HasSuffix(a, "subqueries"), strings.HasSuffix(b, "subqueries"); sa != sb {
				if sa {
					return 1
				}
				return -1
			}
			return strings.Compare(a, b)
		})
		for _, k := range keys {
			w.walk(k, v[k], level)
		}
	}
}

func (w *mysqlPlanWalker) queryBlock(block map[string]any, level int) {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", level) + "Select #")
	if id, ok := planNumber(block["select_id"]); ok {
		b.WriteString(formatNumber(id))
	}
	if cost, ok := mysqlPlanNumber(block, "cost_info", "query_cost"); ok {
		b.WriteString(" | cost " + formatNumber(cost))
	} else if cost, ok := mysqlPlanNumber(block, "cost"); ok {
		b.WriteString(" | cost " + formatNumber(roundTo(cost, 2)))
	}
	if message, ok := block["message"].(string); ok {
		b.WriteString(" | " + message)
	}
	w.operators = append(w.operators, b.String())
}

func (w *mysqlPlanWalker) operation(label string, op map[string]any, level int) {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", level) + label)
	if key, ok := op["sort_key"].(string); ok {
		b.WriteString(" | sort key " + key)
	}
	if op["using_filesort"] == true {
		b.WriteString(" | using filesort")
	}
	if op["using_temporary_table"] == true {
		b.WriteString(" | using temporary table")
	}
	if rows, ok := planNumber(op["r_output_rows"]); ok {
		b.WriteString(" | actual rows " + formatNumber(rows))
	}
	w.operators = append(w.operators, b.String())
}

// table renders one table access, and warns about large full scans and misestimated rows.
func (w *mysqlPlanWalker) table(t map[string]any, level int) {
	name, access := planString(t, "table_name"), planString(t, "access_type")
	title := "Table " + name
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", level) + title + " | access " + access)
	if key, ok := t["key"].(string); ok {
		b.WriteString(" | key " + key)
	}
	if t["using_index"] == true {
		b.WriteString(" | covering index")
	}
	if buffer, ok := t["using_join_buffer"].(string); ok {
		b.WriteString(" | join buffer " + buffer)
	}
	if cost, ok := mysqlPlanNumber(t, "cost_info", "prefix_cost"); ok {
		b.WriteString(" | cost " + formatNumber(cost))
	} else if cost, ok := mysqlPlanNumber(t, "cost"); ok {
		b.WriteString(" | cost " + formatNumber(roundTo(cost, 2)))
	}
	rows, hasRows := planNumber(t["rows_examined_per_scan"])
	if !hasRows {
		rows, hasRows = planNumber(t["rows"])
	}
	filtered, hasFiltered := planNumber(t["filtered"])
	if hasRows {
		b.WriteString(" | est rows " + formatNumber(rows))
	}
	if hasFiltered {
		b.WriteString(" | filtered " + formatNumber(filtered) + "%")
	}
	actualRows, actual := planNumber(t["r_rows"])
	loops, _ := planNumber(t["r_loops"])
	actualFiltered, _ := planNumber(t["r_filtered"])
	switch {
	case actual && loops > 0:
		b.WriteString(" | actual rows " + formatNumber(actualRows) + " x " + formatNumber(loops) + " loops")
		b.WriteString(" | actual filtered " + formatNumber(roundTo(actualFiltered, 2)) + "%")
		time, hasTime := planNumber(t["r_total_time_ms"])
		if !hasTime {
			table, hasTable := planNumber(t["r_table_time_ms"])
			other, hasOther := planNumber(t["r_other_time_ms"])
			time, hasTime = table+other, hasTable || hasOther
		}
		if hasTime {
			b.WriteString(" | time " + formatNumber(roundTo(time, 3)) + " ms")
		}
	case actual:
		b.WriteString(" | never executed")
	}
	condition, hasCondition := t["attached_condition"].(string)
	if indexCondition, ok := t["index_condition"].(string); ok {
		b.WriteString(" | index cond " + indexCondition)
	}
	if hasCondition {
		b.WriteString(" | filter " + condition)
	}
	w.operators = append(w.operators, b.String())

	if !hasFiltered {
		filtered = 100
	}
	if actual && loops > 0 && rowsMisestimated(rows*filtered/100, actualRows*actualFiltered/100) {
		w.warnings = append(w.warnings, title+": estimated "+formatNumber(roundTo(rows*filtered/100, 2))+" rows, got "+
			formatNumber(roundTo(actualRows*actualFiltered/100, 2))+" per loop; stale statistics (ANALYZE TABLE) "+
			"or correlated conditions mislead the optimizer")
	}
	if access != "ALL" && access != "index" {
		return
	}
	scan := "full table scan"
	if access == "index" {
		scan = "full index scan"
	}
	hint := ""
	if hasCondition {
		hint = ": an index matching the filter may help"
	}
	switch {
	case actual && actualRows*loops >= largeScanRows:
		w.warnings = append(w.warnings, title+" ("+scan+") read "+formatNumber(actualRows*loops)+" rows"+hint)
	case !actual && hasRows && rows >= largeScanRows:
		w.warnings = append(w.warnings, title+" ("+scan+") is estimated to read "+formatNumber(rows)+" rows per scan"+hint)
	}
}

// Numbers of MySQL's EXPLAIN ANALYZE tree, e.g. "(cost=15122 rows=150176)" and
// "(actual time=0.428..10.5 rows=150000 loops=1)"; times may read 383e-6.
var (
	treeCost   = regexp.MustCompile(`\(cost=(?:` + treeNum + `\.\.)?(` + treeNum + `) rows=(` + treeNum + `)\)`)
	treeActual = regexp.MustCompile(`\(actual time=(` + treeNum + `)\.\.(` + treeNum + `) rows=(` + treeNum + `) loops=(` + treeNum + `)\)`)
)

const treeNum = `\d+(?:\.\d+)?(?:e[+-]?\d+)?`

// treeNode is one operator of an EXPLAIN ANALYZE tree.
type treeNode struct {
	title                                    string
	cost, rows, startMs, endMs, actual, loop float64
	depth                                    int
	hasCost, hasActual, neverExecuted        bool
}

// summarizeMySQLTree reads MySQL's EXPLAIN ANALYZE tree, and returns its summary and the number
// of rows the statement returned.
func summarizeMySQLTree(document string) (plan StatementPlan, rowCount *int64) {
	var nodes []treeNode
	for line := range strings.SplitSeq(document, "\n") {
		text := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(text, "-> ") {
			continue
		}
		n := treeNode{
			depth: (len(line) - len(text)) / 4, title: strings.TrimPrefix(text, "-> "),
			neverExecuted: strings.HasSuffix(text, "(never executed)"),
		}
		if i := strings.Index(n.title, "  ("); i >= 0 {
			n.title = n.title[:i]
		}
		if v, ok := treeNumbers(treeCost.FindStringSubmatch(text)); ok {
			n.hasCost, n.cost, n.rows = true, v[0], v[1]
		}
		if v, ok := treeNumbers(treeActual.FindStringSubmatch(text)); ok {
			n.hasActual, n.startMs, n.endMs, n.actual, n.loop = true, v[0], v[1], v[2], v[3]
		}
		nodes = append(nodes, n)
	}
	return summarizeTree(nodes)
}

// mysqlJSONNode is one access path of MySQL's JSON plan, version 2 (EXPLAIN ANALYZE FORMAT=JSON):
// the nodes of the text tree, with the same operation titles.
type mysqlJSONNode struct {
	EstimatedRows *float64        `json:"estimated_rows"`
	EstimatedCost *float64        `json:"estimated_total_cost"`
	ActualRows    *float64        `json:"actual_rows"`
	ActualLoops   *float64        `json:"actual_loops"`
	Operation     string          `json:"operation"`
	Inputs        []mysqlJSONNode `json:"inputs"`
	FirstRowMs    float64         `json:"actual_first_row_ms"`
	LastRowMs     float64         `json:"actual_last_row_ms"`
}

// mysqlJSONTree reads MySQL's JSON plan, version 2, as the nodes of the text tree. The plan is the
// document itself, or under query_plan in newer versions.
func mysqlJSONTree(document string) ([]treeNode, error) {
	var plan struct {
		QueryPlan *mysqlJSONNode `json:"query_plan"`
		mysqlJSONNode
	}
	if err := json.Unmarshal([]byte(document), &plan); err != nil {
		return nil, err
	}
	root := &plan.mysqlJSONNode
	if plan.QueryPlan != nil {
		root = plan.QueryPlan
	}
	if root.Operation == "" {
		return nil, errors.New("no operation in the plan")
	}
	var nodes []treeNode
	var walk func(n *mysqlJSONNode, depth int)
	walk = func(n *mysqlJSONNode, depth int) {
		node := treeNode{title: n.Operation, depth: depth}
		if n.EstimatedCost != nil && n.EstimatedRows != nil {
			node.hasCost, node.cost, node.rows = true, treeRound(*n.EstimatedCost), treeRound(*n.EstimatedRows)
		}
		if n.ActualRows != nil && n.ActualLoops != nil {
			node.hasActual, node.actual, node.loop = true, treeRound(*n.ActualRows), *n.ActualLoops
			node.startMs, node.endMs = treeRound(n.FirstRowMs), treeRound(n.LastRowMs)
		} else {
			node.neverExecuted = true
		}
		nodes = append(nodes, node)
		for i := range n.Inputs {
			walk(&n.Inputs[i], depth+1)
		}
	}
	walk(root, 0)
	return nodes, nil
}

// treeRound rounds f as the text tree prints it: whole above 1000, else 3 significant digits.
func treeRound(f float64) float64 {
	if math.Abs(f) >= 1000 {
		return math.Round(f)
	}
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'g', 3, 64), 64) //nolint:errcheck // FormatFloat's output always parses.
	return rounded
}

// summarizeTree summarizes the nodes of an EXPLAIN ANALYZE tree, and returns the number of rows
// the statement returned.
func summarizeTree(nodes []treeNode) (plan StatementPlan, rowCount *int64) {
	s := StatementPlan{Operators: []string{}}
	if len(nodes) > 0 && nodes[0].hasActual {
		rowCount = new(int64(math.Round(nodes[0].actual * nodes[0].loop)))
		s.ExecutionTimeMs = new(nodes[0].endMs)
	}
	estimated := false
	for i := range nodes {
		n := &nodes[i]
		if n.hasCost && !estimated {
			s.EstimatedCost, s.EstimatedRows, estimated = n.cost, n.rows, true
		}
		s.Operators = append(s.Operators, n.line())
		s.Warnings = append(s.Warnings, treeWarnings(nodes, i)...)
	}
	return s, rowCount
}

// treeNumbers parses the numbers of a regexp match (its groups); false without match.
func treeNumbers(match []string) ([]float64, bool) {
	if match == nil {
		return nil, false
	}
	numbers := make([]float64, len(match)-1)
	for i, text := range match[1:] {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, false
		}
		numbers[i] = f
	}
	return numbers, true
}

// line renders n as the PostgreSQL operators are. Rows and times are per loop, as in EXPLAIN ANALYZE.
func (n *treeNode) line() string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", n.depth) + n.title)
	if n.hasCost {
		b.WriteString(" | cost " + formatNumber(n.cost) + " | est rows " + formatNumber(n.rows))
	}
	switch {
	case n.hasActual:
		b.WriteString(" | actual rows " + formatNumber(n.actual) + " x " + formatNumber(n.loop) + " loops")
		b.WriteString(" | time " + formatNumber(n.startMs) + ".." + formatNumber(n.endMs) + " ms")
	case n.neverExecuted:
		b.WriteString(" | never executed")
	}
	return b.String()
}

func (n *treeNode) misestimated() bool {
	return n.hasCost && n.hasActual && n.loop > 0 && rowsMisestimated(n.rows, n.actual)
}

// treeWarnings warns about node i: a misestimate where it starts (the nodes above inherit it),
// and table scans reading many rows.
func treeWarnings(nodes []treeNode, i int) []string {
	n := &nodes[i]
	var children []*treeNode
	for j := i + 1; j < len(nodes) && nodes[j].depth > n.depth; j++ {
		if nodes[j].depth == n.depth+1 {
			children = append(children, &nodes[j])
		}
	}
	var warnings []string
	if n.misestimated() && !slices.ContainsFunc(children, (*treeNode).misestimated) {
		warnings = append(warnings, n.title+": estimated "+formatNumber(n.rows)+" rows, got "+formatNumber(n.actual)+
			" per loop; stale statistics (ANALYZE TABLE) or correlated conditions mislead the optimizer, and the nodes above inherit the error")
	}
	if read := n.actual * n.loop; strings.HasPrefix(n.title, "Table scan on ") && !strings.HasPrefix(n.title, "Table scan on <") && read >= largeScanRows {
		warning := n.title + " read " + formatNumber(read) + " rows"
		if parent := treeParent(nodes, i); parent != nil && strings.HasPrefix(parent.title, "Filter: ") {
			warning += ": an index matching the filter may help"
		}
		warnings = append(warnings, warning)
	}
	return warnings
}

// treeParent is the node above node i; nil for the root.
func treeParent(nodes []treeNode, i int) *treeNode {
	for j := i - 1; j >= 0; j-- {
		if nodes[j].depth < nodes[i].depth {
			return &nodes[j]
		}
	}
	return nil
}
