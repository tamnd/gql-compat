package zu

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/gql-compat/fixture"
)

// Staging is scaffolding: it is work the harness does to hand zu a fixture, and
// zu would not pay for it in use. The report says so, and subtracts it from the
// ingest figure it publishes. That makes it exactly the kind of code whose cost
// nobody watches, which is how planFixture came to spend nineteen seconds
// scanning the node list once per edge endpoint while the SQLite writes it was
// blamed for took a quarter of a second. A run's wall clock is the user's time
// whether or not the report attributes it to the engine, so the cost is pinned
// here.
func TestStagingALargeFixtureIsLinearInIt(t *testing.T) {
	if testing.Short() {
		t.Skip("stages a hundred thousand nodes")
	}
	// The same graph performance/scan/count-all-100k runs on, which is the
	// largest thing this route is asked to carry.
	fx := &fixture.Fixture{Name: "perf-path-100k", Generated: &fixture.Generator{
		Shape: "path", Nodes: 100000, Properties: 2, Seed: 1,
	}}
	if _, err := fx.Materialize(); err != nil {
		t.Fatal(err)
	}

	// Generous by an order of magnitude against the ~0.4s this takes, because a
	// figure tight enough to catch a small regression would be a figure that
	// fails on a loaded CI runner. What it catches is the change in complexity,
	// which is the failure that actually happened.
	// The race build of this has been observed between 17 and 31 seconds on
	// one machine with the same code, because the detector's own bookkeeping
	// is what dominates and it competes with whatever else the run is doing.
	// A budget inside that spread fails on the code it was meant to pass, so
	// the race figure is set above the spread rather than near it.
	budget := 5 * time.Second
	if raceDetector {
		budget = 90 * time.Second
	}
	path := filepath.Join(t.TempDir(), "stage.db")
	start := time.Now()
	if err := writeFixtureDB(context.Background(), path, fx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > budget {
		t.Errorf("staging %d nodes and %d edges took %v, over the %v budget; something in the planner is no longer linear",
			len(fx.Nodes), len(fx.Edges), took.Round(time.Millisecond), budget)
	}
}

// The planner decides which node table every edge binds to, and it now answers
// that from a map built once rather than from a scan per edge. A map keyed by
// node key is only correct if it agrees with the scan it replaced, including on
// the two cases the scan handled by falling through: an unlabelled node, and a
// graph whose edges do not all run between the same label.
func TestThePlannerBindsEachRelTableToItsOwnNodeTable(t *testing.T) {
	fx := &fixture.Fixture{
		Name: "two-shapes",
		Nodes: []fixture.Node{
			{Key: "a", Labels: []string{"Person"}},
			{Key: "b", Labels: []string{"Person"}},
			{Key: "c"},
			{Key: "d"},
		},
		Edges: []fixture.Edge{
			{Type: "KNOWS", From: "a", To: "b"},
			{Type: "NEAR", From: "c", To: "d"},
		},
	}
	plan, err := planFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{"KNOWS": {"Person", "Person"}, "NEAR": {"Node", "Node"}}
	for _, rt := range plan.relTables {
		if w, ok := want[rt.typ]; ok && [2]string{rt.src, rt.dst} != w {
			t.Errorf("rel table %s binds to %s and %s, want %s and %s", rt.typ, rt.src, rt.dst, w[0], w[1])
		}
		delete(want, rt.typ)
	}
	for typ := range want {
		t.Errorf("no rel table for edge type %s", typ)
	}

	// An edge between two labels binds to both of them, one at each end.
	fx.Edges = append(fx.Edges, fixture.Edge{Type: "LIVES_IN", From: "a", To: "c"})
	plan, err = planFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(plan.relTables, func(rt *relTable) bool { return rt.typ == "LIVES_IN" })
	if i < 0 {
		t.Fatal("no rel table for the edge that crosses labels")
	}
	if got := [2]string{plan.relTables[i].src, plan.relTables[i].dst}; got != [2]string{"Person", "Node"} {
		t.Errorf("LIVES_IN binds to %v, want Person and Node", got)
	}

	// One type cannot run between two different pairs of labels, because a
	// rel table names one pair and the second has nowhere to go.
	fx.Edges = append(fx.Edges, fixture.Edge{Type: "LIVES_IN", From: "c", To: "a"})
	if _, err := planFixture(fx); err == nil {
		t.Error("planned an edge type that runs between two different pairs of labels")
	}
}

// A list property crosses SQLite as a JSON array in a column declared with its
// element type, because that is the only place the element type is written
// down: SQLite's storage classes have nothing for a list, so a TEXT column
// holding `[1,2,3]` and one holding the string "[1,2,3]" are the same bytes and
// the declaration is what tells them apart. The empty list is the interesting
// case in both directions. It names no element type, so it takes the one the
// rest of the column has, and a column with nothing but empty lists in it has
// none to take.
func TestAListColumnIsDeclaredWithItsElementType(t *testing.T) {
	fx := &fixture.Fixture{
		Name: "lists",
		Nodes: []fixture.Node{
			{Key: "a", Labels: []string{"P"}, Props: map[string]any{
				"xs": []any{1, 2, 3}, "tags": []any{"one", `a "quoted" one`},
			}},
			{Key: "b", Labels: []string{"P"}, Props: map[string]any{
				"xs": []any{}, "tags": []any{},
			}},
		},
	}
	plan, err := planFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	nt := plan.nodeTables[0]
	want := map[string]string{"tags": "TEXTLIST", "xs": "INTEGERLIST"}
	for i, name := range nt.cols {
		if nt.types[i] != want[name] {
			t.Errorf("column %s is declared %s, want %s", name, nt.types[i], want[name])
		}
	}
	// Column order is sorted, so tags is first and xs is second.
	if got := nt.rows[0][1]; got != "[1,2,3]" {
		t.Errorf("xs staged as %v, want [1,2,3]", got)
	}
	if got := nt.rows[0][0]; got != `["one","a \"quoted\" one"]` {
		t.Errorf("tags staged as %v, want the two strings with the quotes escaped", got)
	}
	if got := nt.rows[1][1]; got != "[]" {
		t.Errorf("an empty list staged as %v, want []", got)
	}

	// A column of nothing but empty lists names no element type. Guessing one
	// would declare a column the fixture never described, and every case
	// against it would be answered about a graph nobody asked for.
	fx.Nodes[0].Props["xs"] = []any{}
	if _, err := planFixture(fx); err == nil {
		t.Error("planned a column of empty lists, which names no element type")
	}

	// An empty list beside a number is a mismatch like any other, and taking
	// the number's type for it would stage the text "[]" in an integer column.
	fx.Nodes[0].Props["xs"] = 1
	if _, err := planFixture(fx); err == nil {
		t.Error("planned a column holding both a number and an empty list")
	}
}

// The two shapes a zu1 list column cannot hold, refused where the column type
// is decided rather than at the point a row is written.
func TestAListZuCannotHoldIsRefused(t *testing.T) {
	cases := []struct {
		name string
		in   []any
	}{
		{"mixed elements", []any{1, "two"}},
		{"a list inside a list", []any{[]any{1}}},
		{"a map inside a list", []any{map[string]any{"a": 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if kind, err := listKind(c.in); err == nil {
				t.Errorf("%v was declared %s; zu1 holds one scalar element type", c.in, kind)
			}
		})
	}
}

// A node that leaves a property out and a node that sets it to null both stage
// as SQL NULL, and neither one gets a say in what type the column is declared.
func TestAMissingOrNullPropertyStagesAsNull(t *testing.T) {
	fx := &fixture.Fixture{
		Name: "nulls",
		Nodes: []fixture.Node{
			{Key: "a", Labels: []string{"P"}, Props: map[string]any{"age": 30, "name": "a"}},
			{Key: "b", Labels: []string{"P"}, Props: map[string]any{"name": "b"}},
			{Key: "c", Labels: []string{"P"}, Props: map[string]any{"age": nil, "name": "c"}},
		},
	}
	plan, err := planFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	nt := plan.nodeTables[0]
	// Column order is sorted, so age is first and name is second.
	if nt.types[0] != "INTEGER" {
		t.Errorf("age is declared %s, want INTEGER from the one row that holds a value", nt.types[0])
	}
	for _, row := range []int{1, 2} {
		if got := nt.rows[row][0]; got != nil {
			t.Errorf("age on node %s staged as %v, want a null", fx.Nodes[row].Key, got)
		}
	}
	if got := nt.rows[0][0]; got != 30 {
		t.Errorf("age on node a staged as %v, want 30", got)
	}

	// A column with a value on no row names no type, and declaring one would
	// answer every case against it about a graph nobody described.
	for i := range fx.Nodes {
		delete(fx.Nodes[i].Props, "age")
	}
	fx.Nodes[0].Props["age"] = nil
	if _, err := planFixture(fx); err == nil {
		t.Error("planned a column that is null on every row, which names no type")
	}
}

// An edge type's properties become columns on its rel table, derived the same
// way a node table's are and staged in the order the edges are written in.
//
// The order is what matters here. zu addresses an edge property column by the
// edge ordinal, the place an edge takes in a load sorted by source and then
// destination, and the converter reads the staged rows back in that order. So
// the row this plans for an edge has to be the row that edge's endpoints are
// written with, not merely the right multiset of values.
func TestEdgePropertiesBecomeColumnsOnTheRelTable(t *testing.T) {
	fx := &fixture.Fixture{
		Name: "weighted",
		Nodes: []fixture.Node{
			{Key: "a", Labels: []string{"P"}},
			{Key: "b", Labels: []string{"P"}},
			{Key: "c", Labels: []string{"P"}},
		},
		Edges: []fixture.Edge{
			{Type: "KNOWS", From: "a", To: "b", Props: map[string]any{"since": 2001, "how": "work"}},
			{Type: "KNOWS", From: "b", To: "c", Props: map[string]any{"since": 2002}},
		},
	}
	plan, err := planFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	rt := plan.relTables[0]
	if got := strings.Join(rt.cols, ","); got != "how,since" {
		t.Fatalf("columns are %s, want the sorted set how,since", got)
	}
	if rt.types[0] != "TEXT" || rt.types[1] != "INTEGER" {
		t.Errorf("columns are declared %v, want [TEXT INTEGER]", rt.types)
	}
	if len(rt.rows) != len(rt.edges) {
		t.Fatalf("%d rows for %d edges", len(rt.rows), len(rt.edges))
	}
	if rt.rows[0][0] != "work" || rt.rows[0][1] != 2001 {
		t.Errorf("the first edge staged as %v, want [work 2001]", rt.rows[0])
	}
	// The second edge sets no how, and an edge without a property stages the
	// same way a node without one does.
	if rt.rows[1][0] != nil || rt.rows[1][1] != 2002 {
		t.Errorf("the second edge staged as %v, want [<nil> 2002]", rt.rows[1])
	}

	// A property that is one type on one edge and another on the next has no
	// column to live in, the same as on a node.
	fx.Edges[1].Props["since"] = "yesterday"
	if _, err := planFixture(fx); err == nil {
		t.Error("planned a column that is an integer on one edge and text on another")
	}
}

// A node with more than one label lives in the table its first label names
// and carries the rest as rows of zu_labels, which is what zu's converter
// turns into the label word each node row holds. The interesting part is that
// nothing else changes: two nodes whose first label is the same share a table
// whatever else they carry, so the column set and the edge endpoints are
// derived exactly as before.
func TestExtraLabelsBecomeRowsOfTheLabelTable(t *testing.T) {
	fx := &fixture.Fixture{
		Name: "multi-label",
		Nodes: []fixture.Node{
			{Key: "a", Labels: []string{"Person", "Employee"}},
			{Key: "b", Labels: []string{"Person"}},
			{Key: "c", Labels: []string{"Person", "Employee", "Manager"}},
			{Key: "d", Labels: []string{"City"}},
		},
		Edges: []fixture.Edge{
			{Type: "KNOWS", From: "a", To: "c"},
			{Type: "LIVES_IN", From: "a", To: "d"},
		},
	}
	plan, err := planFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	person := slices.IndexFunc(plan.nodeTables, func(nt *nodeTable) bool { return nt.label == "Person" })
	if person < 0 {
		t.Fatal("no table for the first label")
	}
	if got := len(plan.nodeTables[person].rows); got != 3 {
		t.Fatalf("Person holds %d rows, want the 3 nodes whose first label it is", got)
	}
	want := [][]string{{"Employee"}, nil, {"Employee", "Manager"}}
	if got := plan.nodeTables[person].extra; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Person carries %v, want %v", got, want)
	}
	// A table nobody gave a second label to carries none, so zu does not
	// grow a word per row for it.
	city := slices.IndexFunc(plan.nodeTables, func(nt *nodeTable) bool { return nt.label == "City" })
	if city < 0 {
		t.Fatal("no table for City")
	}
	if plan.nodeTables[city].extra != nil {
		t.Errorf("City carries %v, want nothing", plan.nodeTables[city].extra)
	}
	// The endpoints are still the tables, not the label sets.
	livesIn := slices.IndexFunc(plan.relTables, func(rt *relTable) bool { return rt.typ == "LIVES_IN" })
	if livesIn < 0 {
		t.Fatal("no rel table for LIVES_IN")
	}
	if got := [2]string{plan.relTables[livesIn].src, plan.relTables[livesIn].dst}; got != [2]string{"Person", "City"} {
		t.Errorf("LIVES_IN binds to %v, want Person and City", got)
	}

	// And the rows reach the file, on the row numbers the node table gave.
	path := filepath.Join(t.TempDir(), "labels.db")
	if err := writeFixtureDB(context.Background(), path, fx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), "SELECT tbl, zrow, label FROM zu_labels ORDER BY tbl, zrow, label")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var tbl, label string
		var zrow int64
		if err := rows.Scan(&tbl, &zrow, &label); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%d/%s", tbl, zrow, label))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantRows := []string{"Person/0/Employee", "Person/2/Employee", "Person/2/Manager"}
	if !slices.Equal(got, wantRows) {
		t.Errorf("zu_labels holds %v, want %v", got, wantRows)
	}
}

// A label has to be a name zu can put in its dictionary, wherever in the set
// it sits: the second one is written into the file the same way the first is.
func TestAnExtraLabelIsHeldToTheSameShapeAsATable(t *testing.T) {
	fx := &fixture.Fixture{
		Name: "bad-label",
		Nodes: []fixture.Node{
			{Key: "a", Labels: []string{"Person", "drop table"}},
			{Key: "b", Labels: []string{"Person"}},
		},
		Edges: []fixture.Edge{{Type: "KNOWS", From: "a", To: "b"}},
	}
	if _, err := planFixture(fx); err == nil {
		t.Error("planned a label that is not a name")
	}
}
