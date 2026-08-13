// Package fixture describes the graphs a conformance case runs against in a
// form no engine owns.
//
// The three engines this harness targets load data in three different ways.
// Neo4j takes Cypher CREATE statements over Bolt. Ladybug takes DDL and then
// COPY. zu takes a two-column edge list and nothing else. A fixture is
// therefore data, never statements: each adapter renders it into whatever its
// engine understands, and an engine that cannot represent some part of a
// fixture says so through its capability set instead of failing the cases
// that use it. A skip that names a missing capability is a measurement; a
// failure caused by the harness speaking the wrong dialect is noise.
package fixture

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Capability is one thing an engine's data model must be able to represent
// before a fixture that uses it can be loaded.
type Capability string

const (
	// CapLabels is the ability to give a node a label at all.
	CapLabels Capability = "labels"
	// CapMultiLabel is the ability to give one node more than one label.
	// Kuzu's node-table-per-label model could not; Ladybug added it.
	CapMultiLabel Capability = "multi-label"
	// CapNodeProperties is the ability to store properties on nodes.
	CapNodeProperties Capability = "node-properties"
	// CapEdgeProperties is the ability to store properties on edges.
	CapEdgeProperties Capability = "edge-properties"
	// CapEdgeTypes is the ability to distinguish edges by type.
	CapEdgeTypes Capability = "edge-types"
	// CapMultipleEdgeTypes is the ability to hold more than one edge type in
	// one graph at once.
	CapMultipleEdgeTypes Capability = "multiple-edge-types"
	// CapMultipleNodeLabels is the ability to hold more than one node label
	// in one graph at once.
	CapMultipleNodeLabels Capability = "multiple-node-labels"
	// CapTemporalValues is the ability to store date and time typed values.
	CapTemporalValues Capability = "temporal-values"
	// CapListValues is the ability to store list-typed properties.
	CapListValues Capability = "list-values"
	// CapNullProperties is the ability to store an explicit null, as opposed
	// to an absent property.
	CapNullProperties Capability = "null-properties"
	// CapFloatValues is the ability to store an approximate numeric property.
	// It is separate from CapNodeProperties because a column-oriented store
	// may hold exact numbers and text and refuse doubles, and an engine that
	// silently rounded one would answer arithmetic cases wrongly rather than
	// decline them.
	CapFloatValues Capability = "float-values"
	// CapBooleanValues is the ability to store a boolean property. Engines
	// that map properties onto a two-type column model — integer and string —
	// have nowhere to put one that does not also change its type.
	CapBooleanValues Capability = "boolean-values"
	// CapUndirectedEdges is the ability to store an edge with no direction.
	// GQL has them; Neo4j does not.
	CapUndirectedEdges Capability = "undirected-edges"
	// CapSelfLoops is the ability to store an edge whose endpoints are equal.
	CapSelfLoops Capability = "self-loops"
	// CapParallelEdges is the ability to store two edges of one type between
	// the same ordered pair of nodes.
	CapParallelEdges Capability = "parallel-edges"
)

// AllCapabilities is every capability a fixture can require, in a stable
// order, so a report can print an engine's data-model surface as a fixed
// column set rather than as whatever this run happened to exercise.
var AllCapabilities = []Capability{
	CapLabels, CapMultiLabel, CapNodeProperties, CapEdgeProperties,
	CapEdgeTypes, CapMultipleEdgeTypes, CapMultipleNodeLabels,
	CapTemporalValues, CapListValues, CapNullProperties,
	CapFloatValues, CapBooleanValues,
	CapUndirectedEdges, CapSelfLoops, CapParallelEdges,
}

// Node is one vertex. Key is the fixture-local identity used by edges and by
// expected results; it is not required to survive into the engine, but every
// adapter must be able to recover a node from it.
type Node struct {
	Key    string         `yaml:"key" json:"key"`
	Labels []string       `yaml:"labels" json:"labels"`
	Props  map[string]any `yaml:"props" json:"props,omitempty"`
}

// Edge is one relationship between two nodes named by their fixture keys.
type Edge struct {
	Key   string         `yaml:"key" json:"key,omitempty"`
	Type  string         `yaml:"type" json:"type"`
	From  string         `yaml:"from" json:"from"`
	To    string         `yaml:"to" json:"to"`
	Props map[string]any `yaml:"props" json:"props,omitempty"`
	// Undirected marks an edge GQL would match in either direction with the
	// undirected pattern. Engines without CapUndirectedEdges cannot hold one.
	Undirected bool `yaml:"undirected" json:"undirected,omitempty"`
}

// Fixture is a whole graph plus the identity every adapter agrees on.
type Fixture struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	Nodes       []Node `yaml:"nodes" json:"nodes"`
	Edges       []Edge `yaml:"edges" json:"edges"`

	// Generated, when set, replaces Nodes and Edges with a synthetic graph
	// built at load time. Conformance cases use literal graphs; the
	// throughput and scaling measurements need graphs too large to write out.
	Generated *Generator `yaml:"generated" json:"generated,omitempty"`

	requires map[Capability]bool
}

// Generator describes a synthetic graph. The shapes are the ones whose
// degree distributions actually change an engine's numbers: a path is the
// worst case for recursion, a clique the worst case for intersection, and a
// power-law graph the one real workloads look like.
type Generator struct {
	// Shape is "path", "cycle", "star", "grid", "clique", "erdos-renyi",
	// "power-law", or "diamonds".
	Shape string `yaml:"shape" json:"shape"`
	// Nodes is the vertex count.
	Nodes int `yaml:"nodes" json:"nodes"`
	// Degree is the mean out-degree, for the shapes that take one.
	Degree int `yaml:"degree" json:"degree,omitempty"`
	// Seed fixes the pseudo-random stream so two runs on two machines build
	// byte-identical graphs and their numbers can be compared.
	Seed int64 `yaml:"seed" json:"seed"`
	// Label and EdgeType name the single node label and edge type used.
	Label    string `yaml:"label" json:"label,omitempty"`
	EdgeType string `yaml:"edge_type" json:"edge_type,omitempty"`
	// Properties adds this many integer properties to each node, so that a
	// scan measurement can be made to read something wider than an id.
	Properties int `yaml:"properties" json:"properties,omitempty"`
}

// Requires computes the capabilities a fixture's data actually needs. It is
// derived rather than declared so a fixture cannot understate itself and get
// loaded into an engine that will silently drop half of it.
func (f *Fixture) Requires() map[Capability]bool {
	if f.requires != nil {
		return f.requires
	}
	req := map[Capability]bool{}
	labels := map[string]bool{}
	types := map[string]bool{}
	pairs := map[string]int{}

	for _, n := range f.Nodes {
		if len(n.Labels) > 0 {
			req[CapLabels] = true
		}
		if len(n.Labels) > 1 {
			req[CapMultiLabel] = true
		}
		for _, l := range n.Labels {
			labels[l] = true
		}
		if len(n.Props) > 0 {
			req[CapNodeProperties] = true
		}
		markValueCaps(n.Props, req)
	}
	for _, e := range f.Edges {
		if e.Type != "" {
			req[CapEdgeTypes] = true
			types[e.Type] = true
		}
		if len(e.Props) > 0 {
			req[CapEdgeProperties] = true
		}
		if e.Undirected {
			req[CapUndirectedEdges] = true
		}
		if e.From == e.To {
			req[CapSelfLoops] = true
		}
		pairs[e.Type+"\x00"+e.From+"\x00"+e.To]++
		markValueCaps(e.Props, req)
	}
	if len(labels) > 1 {
		req[CapMultipleNodeLabels] = true
	}
	if len(types) > 1 {
		req[CapMultipleEdgeTypes] = true
	}
	for _, n := range pairs {
		if n > 1 {
			req[CapParallelEdges] = true
			break
		}
	}
	f.requires = req
	return req
}

// markValueCaps records the capabilities implied by the property values
// themselves rather than by their presence: a temporal value needs temporal
// support even in an engine that is otherwise happy with properties.
func markValueCaps(props map[string]any, req map[Capability]bool) {
	for _, v := range props {
		switch vv := v.(type) {
		case nil:
			req[CapNullProperties] = true
		case []any:
			req[CapListValues] = true
		case bool:
			req[CapBooleanValues] = true
		case float32, float64:
			// YAML gives an unquoted 0.5 a float and an unquoted 2 an int, so
			// the distinction the engine cares about is already made by the
			// time the fixture is parsed. A whole-numbered float stays a float:
			// the fixture author wrote a decimal point, and an engine that can
			// only hold exact numbers cannot honour that either way.
			req[CapFloatValues] = true
		case map[string]any:
			// A typed literal, e.g. {date: "2024-01-01"}.
			for k := range vv {
				switch k {
				case "date", "time", "datetime", "localtime", "localdatetime", "duration":
					req[CapTemporalValues] = true
				}
			}
		case string:
			// Left as a plain string; typed temporals must be spelled with
			// the map form so that a fixture cannot smuggle one past the
			// capability check by looking like text.
			_ = vv
		}
	}
}

// RequiredList returns Requires as a sorted slice, for reports.
func (f *Fixture) RequiredList() []Capability {
	req := f.Requires()
	out := make([]Capability, 0, len(req))
	for c := range req {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// Missing returns the capabilities this fixture needs that have is false, in
// AllCapabilities order. An empty result means the engine can hold the graph.
func (f *Fixture) Missing(has map[Capability]bool) []Capability {
	var out []Capability
	req := f.Requires()
	for _, c := range AllCapabilities {
		if req[c] && !has[c] {
			out = append(out, c)
		}
	}
	return out
}

// Validate checks a fixture is internally consistent before any engine sees
// it: unique keys, edges that name nodes that exist, and no anonymous nodes.
func (f *Fixture) Validate() error {
	if f.Name == "" {
		return errors.New("fixture has no name")
	}
	if f.Generated != nil {
		return f.Generated.validate(f.Name)
	}
	keys := make(map[string]bool, len(f.Nodes))
	for i, n := range f.Nodes {
		if n.Key == "" {
			return fmt.Errorf("fixture %s: node %d has no key", f.Name, i)
		}
		if keys[n.Key] {
			return fmt.Errorf("fixture %s: duplicate node key %q", f.Name, n.Key)
		}
		keys[n.Key] = true
	}
	edgeKeys := map[string]bool{}
	for i, e := range f.Edges {
		if !keys[e.From] {
			return fmt.Errorf("fixture %s: edge %d starts at unknown node %q", f.Name, i, e.From)
		}
		if !keys[e.To] {
			return fmt.Errorf("fixture %s: edge %d ends at unknown node %q", f.Name, i, e.To)
		}
		if e.Key != "" {
			if edgeKeys[e.Key] {
				return fmt.Errorf("fixture %s: duplicate edge key %q", f.Name, e.Key)
			}
			edgeKeys[e.Key] = true
		}
	}
	return nil
}

func (g *Generator) validate(fixture string) error {
	switch g.Shape {
	case "path", "cycle", "star", "grid", "clique", "erdos-renyi", "power-law":
	case "diamonds":
		// The shape only closes on a multiple of three plus one, and a node
		// count that does not close would leave a tail nothing reaches, which
		// is the one thing this fixture must not have: its whole value is that
		// the number of paths across it is known from the node count alone.
		if g.Nodes < 4 || (g.Nodes-1)%3 != 0 {
			return fmt.Errorf("fixture %s: a chain of diamonds has 3k+1 nodes for k of at least one, and %d is not one of those",
				fixture, g.Nodes)
		}
		// 2^k paths. Twenty diamonds is already a million of them, which is a
		// fixture of 61 nodes that can hang an engine that materializes each
		// one, and past that the answer stops fitting anywhere.
		if k := (g.Nodes - 1) / 3; k > 20 {
			return fmt.Errorf("fixture %s: %d diamonds is 2^%d paths, and the answer is the point", fixture, k, k)
		}
	default:
		return fmt.Errorf("fixture %s: unknown generated shape %q", fixture, g.Shape)
	}
	if g.Nodes <= 0 {
		return fmt.Errorf("fixture %s: generated shape needs a positive node count", fixture)
	}
	if g.Shape == "clique" && g.Nodes > 4096 {
		// n^2 edges; 4096 nodes is already 16.7 M edges and the largest
		// clique any measurement here has a use for.
		return fmt.Errorf("fixture %s: clique of %d nodes is too large", fixture, g.Nodes)
	}
	return nil
}

// Set is a named collection of fixtures, indexed for lookup by case id.
type Set struct {
	byName map[string]*Fixture
}

// NewSet indexes fixtures by name, rejecting duplicates and invalid entries.
func NewSet(fixtures []*Fixture) (*Set, error) {
	s := &Set{byName: make(map[string]*Fixture, len(fixtures))}
	for _, f := range fixtures {
		if err := f.Validate(); err != nil {
			return nil, err
		}
		if _, dup := s.byName[f.Name]; dup {
			return nil, fmt.Errorf("duplicate fixture %q", f.Name)
		}
		s.byName[f.Name] = f
	}
	return s, nil
}

// Get returns the fixture with the given name.
func (s *Set) Get(name string) (*Fixture, bool) {
	f, ok := s.byName[name]
	return f, ok
}

// Names returns every fixture name, sorted.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Len reports how many fixtures the set holds.
func (s *Set) Len() int { return len(s.byName) }

// Materialize expands a generated fixture into literal nodes and edges. A
// literal fixture is returned unchanged. The result is cached on the fixture,
// because a scaling run reuses one generated graph across many cases and
// rebuilding a million edges per case would dominate the measurement.
func (f *Fixture) Materialize() (*Fixture, error) {
	if f.Generated == nil {
		return f, nil
	}
	if len(f.Nodes) > 0 {
		return f, nil
	}
	if err := f.Generated.validate(f.Name); err != nil {
		return nil, err
	}
	nodes, edges := f.Generated.build()
	f.Nodes, f.Edges = nodes, edges
	f.requires = nil
	return f, nil
}

// ParseCapabilities turns a list of strings into a capability set, rejecting
// names AllCapabilities does not contain so a typo in an adapter's
// declaration is caught at startup rather than showing up as an unexplained
// skip.
func ParseCapabilities(names []string) (map[Capability]bool, error) {
	known := map[Capability]bool{}
	for _, c := range AllCapabilities {
		known[c] = true
	}
	out := map[Capability]bool{}
	for _, n := range names {
		c := Capability(strings.TrimSpace(n))
		if !known[c] {
			return nil, fmt.Errorf("unknown capability %q", n)
		}
		out[c] = true
	}
	return out, nil
}
