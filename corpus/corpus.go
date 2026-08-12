// Package corpus is the test model: what a conformance case is, what it
// claims to cover, and what counts as passing it.
//
// A case is deliberately not a program. It is a fixture name, one statement
// written in standard GQL, and an expectation. The harness never asks an
// engine to run something the standard does not define, and it never asks a
// case to know which engine is running it. Where an engine has a documented
// non-standard spelling of the same thing, that spelling lives in the case's
// Dialects map and is measured separately, so a report can say both "this
// engine does not accept the GQL form" and "this engine can express the
// meaning" without conflating them.
package corpus

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Kind is what part of the standard a case draws on. It decides which
// scoreboard the case lands in, not how it runs.
type Kind string

const (
	// KindMandatory covers a mandatory subclause of ISO/IEC 39075. Failing
	// one of these means an implementation cannot claim GQL conformance at
	// all, because conformance to the mandatory features is not optional.
	KindMandatory Kind = "mandatory"
	// KindOptional covers one of the 228 optional features in features.xml.
	// Failing one costs that feature and nothing else.
	KindOptional Kind = "optional"
	// KindCondition covers a GQLSTATUS class or subclass from conditions.xml:
	// the engine must not merely reject bad input, it must reject it with the
	// code the standard names.
	KindCondition Kind = "condition"
	// KindGrammar covers a production from the BNF artifact, usually as an
	// accept-or-reject pair. These are the cheapest cases to run and the ones
	// that separate a parser from a language.
	KindGrammar Kind = "grammar"
	// KindPerformance carries no pass criterion beyond completing. It exists
	// so the metrics tables have workloads with known shapes in them.
	KindPerformance Kind = "performance"
)

// AllKinds lists kinds in report order.
var AllKinds = []Kind{KindMandatory, KindOptional, KindCondition, KindGrammar, KindPerformance}

// ExpectKind is how a case's outcome is judged.
type ExpectKind string

const (
	// ExpectRows compares the returned table against Rows.
	ExpectRows ExpectKind = "rows"
	// ExpectEmpty requires a successful result with no rows.
	ExpectEmpty ExpectKind = "empty"
	// ExpectError requires failure, optionally with a specific GQLSTATUS.
	ExpectError ExpectKind = "error"
	// ExpectAccept requires only that the statement was accepted, which is
	// what a grammar case about syntax alone needs.
	ExpectAccept ExpectKind = "accept"
	// ExpectReject requires only that the statement was refused. A parser
	// that accepts what the grammar forbids is not implementing the grammar.
	ExpectReject ExpectKind = "reject"
)

// Expect is the criterion applied to a statement's outcome.
type Expect struct {
	Kind ExpectKind `yaml:"kind" json:"kind"`

	// Columns, when set, must equal the result's column names in order.
	Columns []string `yaml:"columns" json:"columns,omitempty"`
	// Rows is the expected table. Values are compared after normalisation,
	// so an engine returning 1 where another returns 1.0 is not marked wrong
	// for a difference the standard does not require.
	Rows [][]any `yaml:"rows" json:"rows,omitempty"`
	// Unordered compares Rows as a multiset. Set it unless the statement has
	// an ORDER BY; GQL does not promise an order without one, and a harness
	// that demands one is testing an accident.
	Unordered bool `yaml:"unordered" json:"unordered,omitempty"`

	// GQLStatus is the five-character code from conditions.xml the engine
	// should report. Empty means any failure satisfies an ExpectError case.
	GQLStatus string `yaml:"gqlstatus" json:"gqlstatus,omitempty"`
	// ErrorContains, for engines that report no GQLSTATUS, is a substring the
	// message should hold. It is weaker evidence and is reported as such.
	ErrorContains string `yaml:"error_contains" json:"error_contains,omitempty"`
}

// Case is one conformance test.
type Case struct {
	// ID is stable and hierarchical: kind/family/feature/name. Reports,
	// baselines, and skip lists all key on it, so it must never be reused for
	// a different test.
	ID string `yaml:"id" json:"id"`
	// Name is a sentence describing what the case establishes.
	Name string `yaml:"name" json:"name"`
	Kind Kind   `yaml:"kind" json:"kind"`

	// Features are ISO optional feature codes from features.xml.
	Features []string `yaml:"features" json:"features,omitempty"`
	// Subclauses are mandatory subclause numbers, e.g. "14.4". ISO assigns
	// mandatory features no code, so the subclause is the only handle.
	Subclauses []string `yaml:"subclauses" json:"subclauses,omitempty"`
	// Productions are BNF production names from the grammar artifact.
	Productions []string `yaml:"productions" json:"productions,omitempty"`
	// Conditions are GQLSTATUS codes from conditions.xml.
	Conditions []string `yaml:"conditions" json:"conditions,omitempty"`
	// Requires are optional feature codes the case's syntax needs but does not
	// test. A condition case often has to reach its error through an optional
	// construct — there is no mandatory way to write a negative LIMIT — and
	// against an engine that documents the feature as absent the case would
	// report a syntax error where the corpus meant to report a status code.
	// Naming the dependency here lets such an engine be skipped, and skipping
	// is visible in the report where a mis-attributed failure would not be.
	Requires []string `yaml:"requires" json:"requires,omitempty"`

	// Fixture names the graph. Empty means the case needs no data, which is
	// true of most grammar cases.
	Fixture string `yaml:"fixture" json:"fixture,omitempty"`
	// Setup runs before Query, in order, and its results are discarded. Use
	// it for the write statements a read case depends on, never for data a
	// fixture could carry.
	Setup []string `yaml:"setup" json:"setup,omitempty"`
	// Query is the statement under test, in standard GQL.
	Query string `yaml:"query" json:"query"`
	// Params binds named parameters, for the cases about parameters.
	Params map[string]any `yaml:"params" json:"params,omitempty"`

	Expect Expect `yaml:"expect" json:"expect"`

	// Dialects holds an engine's own documented spelling of the same meaning,
	// keyed by adapter name. It is never used to judge conformance. It is run
	// in compatibility mode so a report can distinguish "cannot do this" from
	// "does this, differently".
	Dialects map[string]string `yaml:"dialects" json:"dialects,omitempty"`

	// Tags are free-form selectors for -run filters: "read", "write",
	// "recursive", "temporal", and so on.
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// Repeat overrides the run-wide repetition count for cases whose timing
	// is the point, or whose cost makes the default wasteful.
	Repeat int `yaml:"repeat" json:"repeat,omitempty"`
	// Mutating marks a case whose statements change the graph, so the runner
	// reloads the fixture before it instead of sharing a loaded one.
	Mutating bool `yaml:"mutating" json:"mutating,omitempty"`
	// TimeoutMS overrides the run-wide per-statement timeout.
	TimeoutMS int `yaml:"timeout_ms" json:"timeout_ms,omitempty"`

	// Source records the file the case was read from, for error messages.
	Source string `yaml:"-" json:"source,omitempty"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:[-/][a-z0-9.]+)*$`)

// Validate rejects a case the runner could not interpret unambiguously. It is
// strict on purpose: a case that claims a feature code the standard does not
// define would inflate a conformance score with a test of nothing.
func (c *Case) Validate(known KnownCodes) error {
	where := c.ID
	if where == "" {
		where = "case in " + c.Source
	}
	if c.ID == "" {
		return fmt.Errorf("%s: missing id", where)
	}
	if !idPattern.MatchString(c.ID) {
		return fmt.Errorf("%s: id must be lowercase slash-separated", where)
	}
	if c.Name == "" {
		return fmt.Errorf("%s: missing name", where)
	}
	if c.Query == "" {
		return fmt.Errorf("%s: missing query", where)
	}
	switch c.Kind {
	case KindMandatory, KindOptional, KindCondition, KindGrammar, KindPerformance:
	default:
		return fmt.Errorf("%s: unknown kind %q", where, c.Kind)
	}
	switch c.Expect.Kind {
	case ExpectRows:
		if len(c.Expect.Columns) == 0 {
			return fmt.Errorf("%s: rows expectation needs columns", where)
		}
		for i, r := range c.Expect.Rows {
			if len(r) != len(c.Expect.Columns) {
				return fmt.Errorf("%s: expected row %d has %d values, %d columns declared",
					where, i, len(r), len(c.Expect.Columns))
			}
		}
	case ExpectEmpty, ExpectAccept, ExpectReject:
	case ExpectError:
		if c.Expect.GQLStatus != "" && !known.Status(c.Expect.GQLStatus) {
			return fmt.Errorf("%s: GQLSTATUS %q is not defined in conditions.xml",
				where, c.Expect.GQLStatus)
		}
	default:
		return fmt.Errorf("%s: unknown expect kind %q", where, c.Expect.Kind)
	}

	// A case must claim at least one thing from the standard, or it is not a
	// conformance case and belongs in the performance kind.
	claims := len(c.Features) + len(c.Subclauses) + len(c.Productions) + len(c.Conditions)
	if claims == 0 && c.Kind != KindPerformance {
		return fmt.Errorf("%s: claims no feature, subclause, production, or condition", where)
	}
	for _, f := range c.Features {
		if !known.Feature(f) {
			return fmt.Errorf("%s: %q is not an ISO feature code", where, f)
		}
	}
	for _, f := range c.Requires {
		if !known.Feature(f) {
			return fmt.Errorf("%s: required %q is not an ISO feature code", where, f)
		}
	}
	for _, p := range c.Productions {
		if !known.Production(p) {
			return fmt.Errorf("%s: <%s> is not a production in the ISO grammar", where, p)
		}
	}
	for _, s := range c.Conditions {
		if !known.Status(s) {
			return fmt.Errorf("%s: GQLSTATUS %q is not defined in conditions.xml", where, s)
		}
	}
	for _, s := range c.Subclauses {
		if !known.Subclause(s) {
			return fmt.Errorf("%s: %q is not a subclause of ISO/IEC 39075 that specifies behaviour", where, s)
		}
	}
	if c.Kind == KindMandatory && len(c.Subclauses) == 0 {
		return fmt.Errorf("%s: a mandatory case must name the subclause it covers", where)
	}
	if c.Kind == KindOptional && len(c.Features) == 0 {
		return fmt.Errorf("%s: an optional case must name the feature it covers", where)
	}
	return nil
}

// KnownCodes is the slice of the ISO catalogue validation needs. It is an
// interface so corpus does not import iso, which keeps the case model usable
// in tests that build a catalogue by hand.
type KnownCodes interface {
	Feature(code string) bool
	Production(name string) bool
	Status(code string) bool
	// Subclause reports whether the number names a clause of the standard that
	// specifies behaviour. Front matter and the conformance clause itself are
	// not such clauses, and a case citing one is mis-filed.
	Subclause(number string) bool
}

// Suite is a validated, ordered set of cases.
type Suite struct {
	Cases []*Case
	byID  map[string]*Case
}

// NewSuite validates and indexes cases, rejecting duplicate ids.
func NewSuite(cases []*Case, known KnownCodes) (*Suite, error) {
	s := &Suite{byID: make(map[string]*Case, len(cases))}
	for _, c := range cases {
		if err := c.Validate(known); err != nil {
			return nil, err
		}
		if prev, dup := s.byID[c.ID]; dup {
			return nil, fmt.Errorf("duplicate case id %q (%s and %s)", c.ID, prev.Source, c.Source)
		}
		s.byID[c.ID] = c
		s.Cases = append(s.Cases, c)
	}
	sort.Slice(s.Cases, func(i, j int) bool { return s.Cases[i].ID < s.Cases[j].ID })
	return s, nil
}

// Get returns the case with an id.
func (s *Suite) Get(id string) (*Case, bool) {
	c, ok := s.byID[id]
	return c, ok
}

// Len reports the case count.
func (s *Suite) Len() int { return len(s.Cases) }

// Filter returns the cases matching every constraint given. A zero Selector
// selects everything.
func (s *Suite) Filter(sel Selector) []*Case {
	var out []*Case
	for _, c := range s.Cases {
		if sel.matches(c) {
			out = append(out, c)
		}
	}
	return out
}

// Selector narrows a run. Each field is ANDed; within a field, any match
// counts.
type Selector struct {
	// IDPattern is a regular expression against the case id.
	IDPattern *regexp.Regexp
	// Kinds limits to these kinds.
	Kinds []Kind
	// Features limits to cases claiming one of these ISO feature codes.
	Features []string
	// Tags limits to cases carrying one of these tags.
	Tags []string
	// SkipTags excludes cases carrying any of these tags.
	SkipTags []string
}

func (sel Selector) matches(c *Case) bool {
	if sel.IDPattern != nil && !sel.IDPattern.MatchString(c.ID) {
		return false
	}
	if len(sel.Kinds) > 0 && !slices.Contains(sel.Kinds, c.Kind) {
		return false
	}
	if len(sel.Features) > 0 && !overlaps(sel.Features, c.Features) {
		return false
	}
	if len(sel.Tags) > 0 && !overlaps(sel.Tags, c.Tags) {
		return false
	}
	if len(sel.SkipTags) > 0 && overlaps(sel.SkipTags, c.Tags) {
		return false
	}
	return true
}

func overlaps(want, have []string) bool {
	for _, w := range want {
		for _, h := range have {
			if strings.EqualFold(w, h) {
				return true
			}
		}
	}
	return false
}

// CoveredFeatures returns every ISO feature code the suite claims, sorted.
// The gap between this and the 228 in features.xml is the corpus's own
// coverage debt, and the report prints it rather than hiding it.
func (s *Suite) CoveredFeatures() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, f := range c.Features {
			seen[f] = true
		}
	}
	return sortedKeys(seen)
}

// CoveredSubclauses returns every mandatory subclause the suite claims.
func (s *Suite) CoveredSubclauses() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, x := range c.Subclauses {
			seen[x] = true
		}
	}
	return sortedKeys(seen)
}

// CoveredConditions returns every GQLSTATUS the suite asserts on.
func (s *Suite) CoveredConditions() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, x := range c.Conditions {
			seen[x] = true
		}
		if c.Expect.GQLStatus != "" {
			seen[c.Expect.GQLStatus] = true
		}
	}
	return sortedKeys(seen)
}

// CoveredProductions returns every grammar production the suite claims.
func (s *Suite) CoveredProductions() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, x := range c.Productions {
			seen[x] = true
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
