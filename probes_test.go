package gqlcompat

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/impdef"
)

// set builds a one-probe set without going through the ISO catalogue, which
// checkProbeLabels does not read.
func set(p *impdef.Probe) *impdef.Set {
	return &impdef.Set{Probes: []*impdef.Probe{p}}
}

func fixtures(t *testing.T, f *fixture.Fixture) *fixture.Set {
	t.Helper()
	s, err := fixture.NewSet([]*fixture.Fixture{f})
	if err != nil {
		t.Fatalf("building the fixture set: %v", err)
	}
	return s
}

// A probe matching a label its fixture does not declare is the failure this
// check exists for: it observes nothing, and nothing about observing nothing
// looks like a defect in the harness rather than in the engine.
func TestProbeLabelMustExist(t *testing.T) {
	fxs := fixtures(t, &fixture.Fixture{
		Name:  "numbers",
		Nodes: []fixture.Node{{Key: "v1", Labels: []string{"Datum"}, Props: map[string]any{"n": 1}}},
	})
	err := checkProbeLabels(set(&impdef.Probe{
		ID: "impdef/ia011/inexact-division", Item: "IA011", Kind: impdef.Defined,
		Fixture: "numbers", Statement: "MATCH (v:Value)\nRETURN v.n AS v", Read: impdef.Cell,
	}), fxs)
	if err == nil {
		t.Fatal("a probe naming a label nothing declares loaded")
	}
	if !strings.Contains(err.Error(), `"Value"`) {
		t.Errorf("error %q does not name the label", err)
	}
}

func TestProbeLabelThatExists(t *testing.T) {
	fxs := fixtures(t, &fixture.Fixture{
		Name:  "numbers",
		Nodes: []fixture.Node{{Key: "v1", Labels: []string{"Datum"}, Props: map[string]any{"n": 1}}},
	})
	if err := checkProbeLabels(set(&impdef.Probe{
		ID: "impdef/ia011/inexact-division", Item: "IA011", Kind: impdef.Defined,
		Fixture: "numbers", Statement: "MATCH (v:Datum {n: 1})\nRETURN v.n AS v", Read: impdef.Cell,
	}), fxs); err != nil {
		t.Fatalf("a probe naming the fixture's own label was refused: %v", err)
	}
}

// An edge type is a label as far as this check is concerned, and a property key
// inside a map is not one.
func TestProbeLabelInEdgeAndMap(t *testing.T) {
	fxs := fixtures(t, &fixture.Fixture{
		Name: "pair",
		Nodes: []fixture.Node{
			{Key: "a", Labels: []string{"Person"}, Props: map[string]any{"name": "Alice"}},
			{Key: "b", Labels: []string{"Person"}, Props: map[string]any{"name": "Bob"}},
		},
		Edges: []fixture.Edge{{Key: "e", Type: "KNOWS", From: "a", To: "b"}},
	})
	if err := checkProbeLabels(set(&impdef.Probe{
		ID: "impdef/id022/default-collation", Item: "ID022", Kind: impdef.Defined,
		Fixture:   "pair",
		Statement: "MATCH (p:Person {name: 'Alice'})-[e:KNOWS]->(q:Person)\nRETURN q.name AS v",
		Read:      impdef.Cell,
	}), fxs); err != nil {
		t.Fatalf("a probe over an edge type its fixture declares was refused: %v", err)
	}
	err := checkProbeLabels(set(&impdef.Probe{
		ID: "impdef/id022/default-collation", Item: "ID022", Kind: impdef.Defined,
		Fixture: "pair", Statement: "MATCH (p:Person)-[e:LIKES]->(q:Person)\nRETURN q.name AS v",
		Read: impdef.Cell,
	}), fxs)
	if err == nil {
		t.Fatal("a probe walking an edge type nothing declares loaded")
	}
}

// The shipped probes and the shipped fixtures are one pair, so Load is where
// the check has to hold. This is the test that would have caught the three
// probes that went silent when the fixtures were renamed.
func TestShippedProbesMatchShippedFixtures(t *testing.T) {
	if _, err := Load(); err != nil {
		t.Fatalf("the shipped corpus did not load: %v", err)
	}
}
