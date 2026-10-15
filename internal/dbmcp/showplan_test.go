package dbmcp

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func readShowplan(t *testing.T, name string) StatementPlan {
	t.Helper()
	document, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	statements, err := summarizeShowplan(string(document))
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 1 {
		t.Fatalf("statements = %d, want 1", len(statements))
	}
	return statements[0]
}

func operatorLine(t *testing.T, plan *StatementPlan, prefix string) string {
	t.Helper()
	for _, line := range plan.Operators {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return line
		}
	}
	t.Fatalf("no %s operator in %q", prefix, plan.Operators)
	return ""
}

// The actual plan of a parameterized statement run through sp_executesql (SQL Server 2022).
func TestSummarizeActualShowplan(t *testing.T) {
	plan := readShowplan(t, "showplan_actual.xml")

	if !strings.HasPrefix(plan.Statement, "select TOP 1 ID from Items where ParentID = @p0") {
		t.Errorf("statement = %q", plan.Statement)
	}
	if plan.Optimization != "FULL, GoodEnoughPlanFound" || plan.EstimatedCost != 0.112888 {
		t.Errorf("optimization = %q, cost = %v", plan.Optimization, plan.EstimatedCost)
	}
	if plan.CPUTimeMs == nil || plan.ElapsedTimeMs == nil {
		t.Error("an actual plan must report its CPU and elapsed time")
	}
	if !slices.Contains(plan.Parameters, "@p0 int, compiled for (42), ran with (42)") {
		t.Errorf("parameters = %q", plan.Parameters)
	}
	if plan.MemoryGrant != "1024 KB granted, 16 KB used" {
		t.Errorf("memory grant = %q", plan.MemoryGrant)
	}

	if !strings.HasPrefix(plan.Operators[0], "Top |") || !strings.HasPrefix(plan.Operators[1], "  Nested Loops (Inner Join) |") {
		t.Errorf("operators must be indented by level: %q", plan.Operators[:2])
	}
	seek := operatorLine(t, &plan, "Index Seek")
	for _, want := range []string{"[dbo].[Items].[IX_Parent]", "actual rows 41 in 2 execs", "5 logical reads", "seek ParentID = [@p0]"} {
		if !strings.Contains(seek, want) {
			t.Errorf("index seek %q lacks %q", seek, want)
		}
	}
	lookup := operatorLine(t, &plan, "Clustered Index Seek")
	for _, want := range []string{"cost 97%", "est rows 0.316228 x 41 execs", "actual rows 0 in 41 execs", "71 logical reads",
		"predicate [t].[dbo].[Items].[Deleted] IS NULL AND ltrim(rtrim([t].[dbo].[Items].[Code]))=[@p2]"} {
		if !strings.Contains(lookup, want) {
			t.Errorf("clustered index seek %q lacks %q", lookup, want)
		}
	}
}

// The estimated plan of a statement comparing an nvarchar column to a number.
func TestSummarizeEstimatedShowplan(t *testing.T) {
	plan := readShowplan(t, "showplan_estimated.xml")

	if plan.CPUTimeMs != nil || plan.MemoryGrant != "" {
		t.Errorf("an estimated plan has no measures: cpu %v, memory grant %q", plan.CPUTimeMs, plan.MemoryGrant)
	}
	if len(plan.Warnings) == 0 || !strings.HasPrefix(plan.Warnings[0], "PlanAffectingConvert ConvertIssue=Cardinality Estimate Expression=CONVERT_IMPLICIT(int,") {
		t.Errorf("warnings = %q", plan.Warnings)
	}
	scan := operatorLine(t, &plan, "Clustered Index Scan")
	if strings.Contains(scan, "actual rows") || !strings.Contains(scan, "predicate CONVERT_IMPLICIT(int,[t].[dbo].[Items].[Code],0)=(12)") {
		t.Errorf("clustered index scan = %q", scan)
	}
}

func TestSummarizeShowplanMissingIndexAndOperatorWarnings(t *testing.T) {
	const document = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence><Batch><Statements>
<StmtSimple StatementText="select * from T where A = 1" StatementSubTreeCost="2"><QueryPlan>
<MissingIndexes><MissingIndexGroup Impact="93.5"><MissingIndex Database="[db]" Schema="[dbo]" Table="[T]">
<ColumnGroup Usage="EQUALITY"><Column Name="[A]" ColumnId="2"/></ColumnGroup>
<ColumnGroup Usage="INCLUDE"><Column Name="[B]" ColumnId="3"/><Column Name="[C]" ColumnId="4"/></ColumnGroup>
</MissingIndex></MissingIndexGroup></MissingIndexes>
<RelOp PhysicalOp="Hash Match" LogicalOp="Inner Join" EstimateRows="10" EstimatedTotalSubtreeCost="2">
<Warnings><SpillToTempDb SpillLevel="1"/><ColumnsWithNoStatistics><ColumnReference Table="[T]" Column="A"/></ColumnsWithNoStatistics></Warnings>
<Hash><RelOp PhysicalOp="Table Scan" LogicalOp="Table Scan" EstimateRows="100" EstimatedTotalSubtreeCost="1.5">
<TableScan><Object Schema="[dbo]" Table="[T]"/></TableScan></RelOp></Hash></RelOp>
</QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

	statements, err := summarizeShowplan(document)
	if err != nil {
		t.Fatal(err)
	}
	plan := statements[0]
	if want := []string{"impact 93.5%: [dbo].[T] EQUALITY: [A] INCLUDE: [B] [C]"}; !slices.Equal(plan.MissingIndexes, want) {
		t.Errorf("missing indexes = %q, want %q", plan.MissingIndexes, want)
	}
	if want := []string{
		"Hash Match (Inner Join) | cost 25% | est rows 10 | WARNINGS SpillToTempDb SpillLevel=1; ColumnsWithNoStatistics [T].A",
		"  Table Scan [dbo].[T] | cost 75% | est rows 100",
	}; !slices.Equal(plan.Operators, want) {
		t.Errorf("operators = %q, want %q", plan.Operators, want)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("operator warnings leaked to the statement: %q", plan.Warnings)
	}
}

func TestSummarizeShowplanRejectsInvalidXML(t *testing.T) {
	if _, err := summarizeShowplan("<ShowPlanXML><Batch>"); err == nil {
		t.Fatal("expected an error for a truncated document")
	}
}
