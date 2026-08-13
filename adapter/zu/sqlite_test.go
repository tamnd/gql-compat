package zu

import (
	"context"
	"path/filepath"
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
	budget := 5 * time.Second
	if raceDetector {
		budget = 30 * time.Second
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
	want := map[string]string{"KNOWS": "Person", "NEAR": "Node"}
	for _, rt := range plan.relTables {
		if w, ok := want[rt.typ]; ok && rt.endpoint != w {
			t.Errorf("rel table %s binds to %s, want %s", rt.typ, rt.endpoint, w)
		}
		delete(want, rt.typ)
	}
	for typ := range want {
		t.Errorf("no rel table for edge type %s", typ)
	}

	// An edge between two labels has nowhere to go, and the planner has to say
	// so rather than write it into whichever table it saw first.
	fx.Edges = append(fx.Edges, fixture.Edge{Type: "LIVES_IN", From: "a", To: "c"})
	if _, err := planFixture(fx); err == nil {
		t.Error("planned an edge that crosses labels; zu binds a rel table to one node table")
	}
}
