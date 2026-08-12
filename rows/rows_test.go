package rows_test

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/rows"
)

// What this package decides is whether an engine got a query right, so every
// test here is really a statement about where the line falls between "a
// different answer" and "the same answer spelled differently by a driver".
// The rule the corpus depends on: values, their order where an ORDER BY
// demands one, and column names are the standard's; representation is not.

func tbl(cols []string, rs ...[]any) *rows.Table {
	return &rows.Table{Columns: cols, Rows: rs}
}

func TestIdenticalTablesMatch(t *testing.T) {
	a := tbl([]string{"name", "age"}, []any{"Ada", 36}, []any{"Bob", 41})
	b := tbl([]string{"name", "age"}, []any{"Ada", 36}, []any{"Bob", 41})
	if d := rows.Compare(a, b, rows.Options{StrictColumns: true}); d != nil {
		t.Fatalf("identical tables differ: %s", d.Error())
	}
}

func TestOrderMattersOnlyWhenTheCaseSaysSo(t *testing.T) {
	want := tbl([]string{"n"}, []any{1}, []any{2})
	got := tbl([]string{"n"}, []any{2}, []any{1})

	// Without ORDER BY the standard promises nothing about order, so a case
	// that compared ordered would be scoring a coin toss.
	if d := rows.Compare(want, got, rows.Options{Unordered: true}); d != nil {
		t.Errorf("an unordered comparison rejected a permutation: %s", d.Error())
	}
	// With ORDER BY the order is the answer.
	if d := rows.Compare(want, got, rows.Options{}); d == nil {
		t.Error("an ordered comparison accepted a permutation")
	}
}

func TestIntegerAndDoubleAreTheSameAnswerUnlessTheCaseIsAboutTypes(t *testing.T) {
	// A driver that hands back 30 as a double has not failed a conformance
	// test; it has a different binding.
	want := tbl([]string{"v"}, []any{30})
	got := tbl([]string{"v"}, []any{30.0})
	if d := rows.Compare(want, got, rows.Options{}); d != nil {
		t.Errorf("30 and 30.0 compared unequal: %s", d.Error())
	}
	if d := rows.Compare(want, got, rows.Options{StrictTypes: true}); d == nil {
		t.Error("a case about the integer/double distinction saw no distinction")
	}
}

func TestToleranceIsZeroUnlessAsked(t *testing.T) {
	// The corpus never sets a tolerance: a tolerance is a policy about
	// precision and ISO specifies none. This is the test that keeps a default
	// from being introduced quietly.
	want := tbl([]string{"v"}, []any{1.0})
	got := tbl([]string{"v"}, []any{1.0000001})
	if d := rows.Compare(want, got, rows.Options{}); d == nil {
		t.Error("two different doubles compared equal with no tolerance set")
	}
	if d := rows.Compare(want, got, rows.Options{FloatTolerance: 1e-6}); d != nil {
		t.Errorf("a 1e-7 difference exceeded a 1e-6 tolerance: %s", d.Error())
	}
}

func TestNullIsNotZeroAndNotEmptyString(t *testing.T) {
	null := tbl([]string{"v"}, []any{nil})
	for _, other := range []any{0, "", false} {
		if d := rows.Compare(null, tbl([]string{"v"}, []any{other}), rows.Options{}); d == nil {
			t.Errorf("null compared equal to %#v", other)
		}
	}
	if d := rows.Compare(null, tbl([]string{"v"}, []any{nil}), rows.Options{}); d != nil {
		t.Errorf("null did not compare equal to null: %s", d.Error())
	}
}

func TestShapeDifferencesAreReportedBeforeValues(t *testing.T) {
	want := tbl([]string{"a"}, []any{1}, []any{2})
	got := tbl([]string{"a"}, []any{1})
	d := rows.Compare(want, got, rows.Options{})
	if d == nil {
		t.Fatal("a table with a row missing compared equal")
	}
	if !strings.Contains(d.Reason, "row count") {
		t.Errorf("reason %q should name the row count", d.Reason)
	}
	// A shape difference is about the table, not a cell, so it must not point
	// at a row that a reader would then go looking for.
	if d.Row != -1 {
		t.Errorf("a row-count difference located itself at row %d", d.Row)
	}
}

func TestColumnNamesAreCheckedOnlyWhenTheCaseNamesThem(t *testing.T) {
	want := tbl([]string{"name"}, []any{"Ada"})
	got := tbl([]string{"n"}, []any{"Ada"})
	if d := rows.Compare(want, got, rows.Options{StrictColumns: true}); d == nil {
		t.Error("a renamed projection passed a strict-column comparison")
	}
	if d := rows.Compare(want, got, rows.Options{}); d != nil {
		t.Errorf("a case about values failed over a column name: %s", d.Error())
	}
}

func TestNodesCompareByLabelsAndPropertiesNotByIdentity(t *testing.T) {
	// Element ids are implementation-dependent; ISO says so. Comparing them
	// would fail every engine for being itself.
	want := tbl([]string{"p"}, []any{rows.Node{Labels: []string{"Person"}, Props: map[string]any{"name": "Ada"}}})
	got := tbl([]string{"p"}, []any{rows.Node{Labels: []string{"Person"}, Props: map[string]any{"name": "Ada"}}})
	if d := rows.Compare(want, got, rows.Options{}); d != nil {
		t.Errorf("two equal nodes differed: %s", d.Error())
	}

	// Label order is not meaningful either: GQL labels are a set.
	reordered := tbl([]string{"p"}, []any{rows.Node{Labels: []string{"Employee", "Person"}, Props: map[string]any{"name": "Ada"}}})
	twoLabels := tbl([]string{"p"}, []any{rows.Node{Labels: []string{"Person", "Employee"}, Props: map[string]any{"name": "Ada"}}})
	if d := rows.Compare(twoLabels, reordered, rows.Options{}); d != nil {
		t.Errorf("label order changed the answer: %s", d.Error())
	}

	// A different property is a different answer.
	other := tbl([]string{"p"}, []any{rows.Node{Labels: []string{"Person"}, Props: map[string]any{"name": "Bob"}}})
	if d := rows.Compare(want, other, rows.Options{}); d == nil {
		t.Error("nodes with different properties compared equal")
	}
}

func TestNormalizeCollapsesTheDriversNumericTypes(t *testing.T) {
	// YAML gives int, JSON gives float64, Bolt gives int64. Without this an
	// expectation would have to be written once per driver.
	for _, v := range []any{int(7), int8(7), int16(7), int32(7), int64(7)} {
		if got := rows.Normalize(v); got != int64(7) {
			t.Errorf("Normalize(%T %v) = %T %v, want int64 7", v, v, got, got)
		}
	}
	if got := rows.Normalize(float32(0.5)); got != float64(0.5) {
		t.Errorf("Normalize(float32 0.5) = %#v", got)
	}
}

func TestMissingResultIsADifferenceAndNotAPanic(t *testing.T) {
	d := rows.Compare(tbl([]string{"v"}, []any{1}), nil, rows.Options{})
	if d == nil {
		t.Fatal("a nil table compared equal to a table with a row")
	}
	if d.Error() == "" {
		t.Error("the diff renders to nothing, so a report would show a blank reason")
	}
}
