package fixture_test

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/fixture"
)

// The two things this package must get right are both load-bearing for every
// number the harness publishes. Requires decides which engines are asked to
// hold a graph — understate it and an engine silently drops half the data and
// then fails cases about the half that is missing. The generator decides what
// a throughput figure was measured on — let it drift between runs and the
// matrix compares graphs rather than engines.

func has(t *testing.T, f *fixture.Fixture, want ...fixture.Capability) {
	t.Helper()
	req := f.Requires()
	for _, c := range want {
		if !req[c] {
			t.Errorf("fixture %s does not require %s; it holds data that needs it", f.Name, c)
		}
	}
}

func lacks(t *testing.T, f *fixture.Fixture, unwanted ...fixture.Capability) {
	t.Helper()
	req := f.Requires()
	for _, c := range unwanted {
		if req[c] {
			t.Errorf("fixture %s requires %s, which nothing in it needs; engines will skip cases they could run", f.Name, c)
		}
	}
}

func TestPlainGraphRequiresOnlyWhatItUses(t *testing.T) {
	f := &fixture.Fixture{
		Name:  "plain",
		Nodes: []fixture.Node{{Key: "a", Labels: []string{"Person"}}, {Key: "b", Labels: []string{"Person"}}},
		Edges: []fixture.Edge{{Type: "KNOWS", From: "a", To: "b"}},
	}
	has(t, f, fixture.CapLabels, fixture.CapEdgeTypes)
	// One label used twice is not multiple labels, and two nodes with no
	// properties do not need property support.
	lacks(t, f,
		fixture.CapMultiLabel, fixture.CapMultipleNodeLabels, fixture.CapMultipleEdgeTypes,
		fixture.CapNodeProperties, fixture.CapEdgeProperties,
		fixture.CapSelfLoops, fixture.CapParallelEdges, fixture.CapUndirectedEdges)
}

func TestMultiLabelIsPerNodeAndMultipleLabelsIsPerGraph(t *testing.T) {
	// The distinction matters: Kuzu's node-table-per-label model could hold a
	// graph with Person and Company nodes but not a node that is both.
	twoKinds := &fixture.Fixture{
		Name:  "two-kinds",
		Nodes: []fixture.Node{{Key: "a", Labels: []string{"Person"}}, {Key: "b", Labels: []string{"Company"}}},
	}
	has(t, twoKinds, fixture.CapMultipleNodeLabels)
	lacks(t, twoKinds, fixture.CapMultiLabel)

	both := &fixture.Fixture{
		Name:  "both",
		Nodes: []fixture.Node{{Key: "a", Labels: []string{"Person", "Employee"}}},
	}
	has(t, both, fixture.CapMultiLabel, fixture.CapMultipleNodeLabels)
}

func TestTemporalMustBeSpelledAsATypedLiteral(t *testing.T) {
	// A date written as a bare string is a string. If it counted as temporal,
	// every fixture with a name in it would demand temporal support; if the map
	// form did not count, a fixture could smuggle a date past an engine that
	// cannot store one.
	text := &fixture.Fixture{
		Name:  "text",
		Nodes: []fixture.Node{{Key: "a", Props: map[string]any{"born": "2024-01-15"}}},
	}
	lacks(t, text, fixture.CapTemporalValues)

	typed := &fixture.Fixture{
		Name:  "typed",
		Nodes: []fixture.Node{{Key: "a", Props: map[string]any{"born": map[string]any{"date": "2024-01-15"}}}},
	}
	has(t, typed, fixture.CapTemporalValues, fixture.CapNodeProperties)
}

func TestValueShapesOnEdgesCountToo(t *testing.T) {
	f := &fixture.Fixture{
		Name:  "edge-values",
		Nodes: []fixture.Node{{Key: "a"}, {Key: "b"}},
		Edges: []fixture.Edge{{Type: "T", From: "a", To: "b", Props: map[string]any{
			"tags":  []any{"x", "y"},
			"note":  nil,
			"since": map[string]any{"datetime": "2024-01-15T00:00:00Z"},
		}}},
	}
	has(t, f,
		fixture.CapEdgeProperties, fixture.CapListValues,
		fixture.CapNullProperties, fixture.CapTemporalValues)
	// Properties on edges say nothing about properties on nodes.
	lacks(t, f, fixture.CapNodeProperties)
}

func TestTopologyCapabilitiesAreDerivedFromTheEdges(t *testing.T) {
	f := &fixture.Fixture{
		Name:  "shapes",
		Nodes: []fixture.Node{{Key: "a"}, {Key: "b"}},
		Edges: []fixture.Edge{
			{Type: "T", From: "a", To: "a"},
			{Type: "T", From: "a", To: "b"},
			{Type: "T", From: "a", To: "b"},
			{Type: "U", From: "b", To: "a", Undirected: true},
		},
	}
	has(t, f,
		fixture.CapSelfLoops, fixture.CapParallelEdges,
		fixture.CapUndirectedEdges, fixture.CapMultipleEdgeTypes)
}

func TestParallelEdgesNeedTheSameTypeAndDirection(t *testing.T) {
	// Two differently typed edges between one pair are not parallel edges, and
	// neither is one edge each way. Calling them parallel would skip cases on
	// engines that can hold them perfectly well.
	f := &fixture.Fixture{
		Name:  "not-parallel",
		Nodes: []fixture.Node{{Key: "a"}, {Key: "b"}},
		Edges: []fixture.Edge{
			{Type: "T", From: "a", To: "b"},
			{Type: "U", From: "a", To: "b"},
			{Type: "T", From: "b", To: "a"},
		},
	}
	lacks(t, f, fixture.CapParallelEdges)
}

func TestMissingNamesOnlyTheGapsAndInAStableOrder(t *testing.T) {
	f := &fixture.Fixture{
		Name:  "needs-three",
		Nodes: []fixture.Node{{Key: "a", Labels: []string{"P", "Q"}, Props: map[string]any{"t": []any{1}}}},
	}
	all := map[fixture.Capability]bool{}
	for _, c := range fixture.AllCapabilities {
		all[c] = true
	}
	if got := f.Missing(all); len(got) != 0 {
		t.Errorf("an engine with every capability is missing %v", got)
	}

	partial := map[fixture.Capability]bool{fixture.CapLabels: true, fixture.CapNodeProperties: true}
	got := f.Missing(partial)
	want := []fixture.Capability{fixture.CapMultiLabel, fixture.CapMultipleNodeLabels, fixture.CapListValues}
	if len(got) != len(want) {
		t.Fatalf("missing %v, want %v", got, want)
	}
	// The order is AllCapabilities order, not map order, or the same skip would
	// print differently on every run and reports would not diff.
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing %v, want %v", got, want)
		}
	}
}

func TestValidateCatchesTheMistakesAFixtureAuthorMakes(t *testing.T) {
	cases := []struct {
		name string
		f    *fixture.Fixture
		want string
	}{
		{"no name", &fixture.Fixture{}, "no name"},
		{"no key", &fixture.Fixture{Name: "f", Nodes: []fixture.Node{{}}}, "no key"},
		{
			"duplicate key",
			&fixture.Fixture{Name: "f", Nodes: []fixture.Node{{Key: "a"}, {Key: "a"}}},
			"duplicate node key",
		},
		{
			"dangling edge",
			&fixture.Fixture{Name: "f", Nodes: []fixture.Node{{Key: "a"}}, Edges: []fixture.Edge{{From: "a", To: "z"}}},
			"unknown node",
		},
		{
			"unknown shape",
			&fixture.Fixture{Name: "f", Generated: &fixture.Generator{Shape: "hairball", Nodes: 10}},
			"unknown generated shape",
		},
		{
			"empty generated graph",
			&fixture.Fixture{Name: "f", Generated: &fixture.Generator{Shape: "path"}},
			"positive node count",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.f.Validate()
			if err == nil {
				t.Fatalf("accepted a fixture with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should say %q, or the author cannot find the problem", err, tc.want)
			}
		})
	}
}

func TestGeneratedShapesHaveTheEdgeCountsTheirNamesPromise(t *testing.T) {
	cases := []struct {
		shape string
		nodes int
		edges int
	}{
		{"path", 100, 99},
		{"cycle", 100, 100},
		{"star", 100, 99},
		{"clique", 20, 20 * 19},
	}
	for _, tc := range cases {
		t.Run(tc.shape, func(t *testing.T) {
			f := &fixture.Fixture{Name: tc.shape, Generated: &fixture.Generator{Shape: tc.shape, Nodes: tc.nodes, Seed: 1}}
			m, err := f.Materialize()
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Nodes) != tc.nodes {
				t.Errorf("%d nodes, want %d", len(m.Nodes), tc.nodes)
			}
			if len(m.Edges) != tc.edges {
				t.Errorf("%d edges, want %d", len(m.Edges), tc.edges)
			}
		})
	}
}

func TestTheSameSeedBuildsTheSameGraphOnAnyMachine(t *testing.T) {
	// This is the property that makes cross-machine throughput numbers mean
	// anything. Both random shapes are checked because they draw differently.
	for _, shape := range []string{"erdos-renyi", "power-law"} {
		t.Run(shape, func(t *testing.T) {
			// Generated edges carry no properties, so type and endpoints are
			// the whole edge.
			same := func(x, y fixture.Edge) bool {
				return x.Type == y.Type && x.From == y.From && x.To == y.To
			}
			build := func(seed int64) []fixture.Edge {
				f := &fixture.Fixture{Name: shape, Generated: &fixture.Generator{
					Shape: shape, Nodes: 200, Degree: 4, Seed: seed,
				}}
				m, err := f.Materialize()
				if err != nil {
					t.Fatal(err)
				}
				return m.Edges
			}
			a, b := build(7), build(7)
			if len(a) != len(b) {
				t.Fatalf("seed 7 built %d edges once and %d the next time", len(a), len(b))
			}
			for i := range a {
				if !same(a[i], b[i]) {
					t.Fatalf("seed 7 built different graphs: edge %d is %v then %v", i, a[i], b[i])
				}
			}
			// And a different seed must actually give a different graph, or the
			// stream is not being seeded at all and this test proves nothing.
			c := build(8)
			identical := len(a) == len(c)
			if identical {
				for i := range a {
					if !same(a[i], c[i]) {
						identical = false
						break
					}
				}
			}
			if identical {
				t.Error("seeds 7 and 8 built identical graphs; the seed is not reaching the generator")
			}
		})
	}
}

func TestMaterializeIsIdempotentSoAScalingRunBuildsOnce(t *testing.T) {
	f := &fixture.Fixture{Name: "big", Generated: &fixture.Generator{Shape: "path", Nodes: 1000, Seed: 3}}
	first, err := f.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	if &first.Nodes[0] != &second.Nodes[0] {
		t.Error("the second Materialize rebuilt the graph; a scaling run would pay for it once per case")
	}
}

func TestGeneratedPropertiesArePresentAndPredictable(t *testing.T) {
	f := &fixture.Fixture{Name: "props", Generated: &fixture.Generator{
		Shape: "path", Nodes: 50, Seed: 1, Properties: 2,
	}}
	m, err := f.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	n := m.Nodes[42]
	if n.Props["id"] != int64(42) {
		t.Errorf("node 42 has id %#v; expected results sort by it", n.Props["id"])
	}
	// p0 splits the graph in ten and p1 in a hundred, which is what makes a
	// filter's selectivity known in advance rather than measured after.
	if n.Props["p0"] != int64(2) || n.Props["p1"] != int64(42) {
		t.Errorf("node 42 property values %#v %#v, want 2 and 42", n.Props["p0"], n.Props["p1"])
	}
	// Requires must be recomputed after materialising, or a generated fixture
	// answers for the empty graph it was before.
	has(t, m, fixture.CapLabels, fixture.CapNodeProperties, fixture.CapEdgeTypes)
}

func TestSetRejectsDuplicatesAndInvalidMembers(t *testing.T) {
	good := &fixture.Fixture{Name: "a", Nodes: []fixture.Node{{Key: "x"}}}
	if _, err := fixture.NewSet([]*fixture.Fixture{good, {Name: "a"}}); err == nil {
		t.Error("a set accepted two fixtures called \"a\"; a case naming it would get whichever won")
	}
	if _, err := fixture.NewSet([]*fixture.Fixture{{Name: "b", Nodes: []fixture.Node{{}}}}); err == nil {
		t.Error("a set accepted a fixture that does not validate")
	}
	s, err := fixture.NewSet([]*fixture.Fixture{good})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("a"); !ok {
		t.Error("the fixture that was added cannot be looked up")
	}
	if _, ok := s.Get("nope"); ok {
		t.Error("an absent fixture was found")
	}
	if s.Len() != 1 || len(s.Names()) != 1 {
		t.Errorf("set holds %d fixtures, want 1", s.Len())
	}
}

func TestParseCapabilitiesRejectsATypo(t *testing.T) {
	// A misspelled capability in an adapter would otherwise turn into an
	// engine that silently declines every case using it, reported as a skip
	// nobody can explain.
	if _, err := fixture.ParseCapabilities([]string{"multi-labels"}); err == nil {
		t.Error("a misspelled capability was accepted")
	}
	got, err := fixture.ParseCapabilities([]string{"labels", " self-loops "})
	if err != nil {
		t.Fatal(err)
	}
	if !got[fixture.CapLabels] || !got[fixture.CapSelfLoops] {
		t.Errorf("parsed %v, want labels and self-loops", got)
	}
}
