package runner_test

import (
	"strings"
	"testing"
	"testing/fstest"

	gqlcompat "github.com/tamnd/gql-compat"
	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/adapter/fake"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/runner"
)

// A limit case is the one place the corpus cannot write the statement it wants
// to send. ISO names the condition and leaves the threshold to the engine, so
// a fixed number in the query reaches the condition on the engines that happen
// to draw the line below it and measures nothing on the rest. What these tests
// hold down is that the number comes from the engine's own declaration, that
// one unit past it is what gets sent, and that an engine declaring nothing is
// skipped rather than sent a guess.

const scaledCorpus = `
cases:
  - id: condition/22g0s/scaled
    name: A node past the property maximum raises the node property maximum
    kind: condition
    conditions: ["22G0S"]
    subclauses: ["13.2"]
    mutating: true
    limit: IL002
    scale:
      kind: node
      each: "p<<n>>: <<n>>"
      between: ", "
    query: |
      INSERT (:Wide {<<scale>>})
    expect:
      kind: error
      gqlstatus: "22G0S"
`

// scaledEngine answers every statement by counting the properties in it and
// refusing anything over max, which is what an engine with that limit does.
func scaledEngine(t *testing.T, max int, declare bool) (adapter.Driver, *string) {
	t.Helper()
	data := make(map[fixture.Capability]bool, len(fixture.AllCapabilities))
	for _, c := range fixture.AllCapabilities {
		data[c] = true
	}
	caps := adapter.Capabilities{Data: data, GQLStatus: true}
	if declare {
		caps.Limits = map[string]int{"IL002/node": max}
	}
	var seen string
	return fake.New(fake.Config{
		Capabilities: caps,
		Respond: func(stmt string) (fake.Answer, bool) {
			seen = stmt
			if strings.Count(stmt, ":") > max {
				return fake.Answer{Failure: &adapter.Failure{
					GQLStatus: "22G0S", Message: "too many properties"}}, true
			}
			return fake.Answer{}, true
		},
	}), &seen
}

func runScaled(t *testing.T, d adapter.Driver) *runner.CaseResult {
	t.Helper()
	std, err := gqlcompat.LoadFS(fstest.MapFS{"scaled.yaml": &fstest.MapFile{Data: []byte(scaledCorpus)}})
	if err != nil {
		t.Fatalf("loading the scaled corpus: %v", err)
	}
	rep, err := std.Run(t.Context(), d, runner.Config{Repeats: 1, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(rep.Cases) != 1 {
		t.Fatalf("expected one case, got %d", len(rep.Cases))
	}
	return &rep.Cases[0]
}

func TestAScaledCaseIsBuiltOnePastTheDeclaredMaximum(t *testing.T) {
	d, seen := scaledEngine(t, 5, true)
	r := runScaled(t, d)
	if r.Outcome != runner.Pass {
		t.Fatalf("outcome %s (%s), want pass: the engine refused the statement its own limit says it must", r.Outcome, r.Reason)
	}
	if r.ScaledTo != 6 {
		t.Errorf("scaled to %d, want 6: a maximum of five is reached by asking for six", r.ScaledTo)
	}
	// The label colon is not a property, so six properties is seven colons.
	if got := strings.Count(*seen, ":"); got != 7 {
		t.Errorf("the statement carried %d colons, want 7: %q", got, *seen)
	}
	if !strings.Contains(*seen, "p6: 6") || strings.Contains(*seen, "p7: 7") {
		t.Errorf("the statement should stop at the sixth property, got %q", *seen)
	}
}

// The report keeps the template rather than the expansion. A run against an
// engine holding four thousand properties writes a statement tens of thousands
// of characters wide, and a CSV where one field is longer than every other row
// put together is one nobody opens. The template and the count reproduce it.
func TestAScaledCaseReportsTheTemplateAndTheCount(t *testing.T) {
	d, _ := scaledEngine(t, 5, true)
	r := runScaled(t, d)
	if !strings.Contains(r.Statement, "<<scale>>") {
		t.Errorf("the report should keep the template, got %q", r.Statement)
	}
	if strings.Contains(r.Statement, "p1: 1") {
		t.Errorf("the report should not keep the expansion, got %q", r.Statement)
	}
}

// An engine that declares no maximum for the item has no size a statement can
// ask for that reaches the condition, and guessing one would produce an
// ordinary answer to an ordinary statement.
func TestAnEngineThatDeclaresNoMaximumSkipsTheCase(t *testing.T) {
	d, seen := scaledEngine(t, 5, false)
	r := runScaled(t, d)
	if r.Outcome != runner.Skip || r.Skip != runner.SkipWithinLimit {
		t.Fatalf("outcome %s skip %q, want a within-limit skip", r.Outcome, r.Skip)
	}
	// The runner's own probe is the only thing the engine should have seen.
	if strings.Contains(*seen, "INSERT") {
		t.Errorf("the case statement should never have been sent, got %q", *seen)
	}
	if !strings.Contains(r.Reason, "IL002/node") {
		t.Errorf("the reason should name the item and the kind, got %q", r.Reason)
	}
	if r.WantStatus != "22G0S" {
		t.Errorf("want status %q: a skipped condition case still records which code went untested", r.WantStatus)
	}
}

// Nodes and edges are separate items in ISO 24.5.2, and an engine may hold
// four thousand properties on one and refuse them on the other. A declaration
// for the wrong kind is no declaration at all.
func TestTheDeclaredMaximumIsReadPerKind(t *testing.T) {
	data := make(map[fixture.Capability]bool, len(fixture.AllCapabilities))
	for _, c := range fixture.AllCapabilities {
		data[c] = true
	}
	d := fake.New(fake.Config{Capabilities: adapter.Capabilities{
		Data:      data,
		GQLStatus: true,
		Limits:    map[string]int{"IL002/edge": 5},
	}})
	r := runScaled(t, d)
	if r.Outcome != runner.Skip || r.Skip != runner.SkipWithinLimit {
		t.Fatalf("outcome %s skip %q, want a within-limit skip: the edge limit says nothing about nodes", r.Outcome, r.Skip)
	}
}

// A condition ISO names only for engines lacking a feature is a different
// thing from a condition behind a threshold, and reading one as the other
// tells a maintainer to go and write a case that cannot exist.
const unlessCorpus = `
cases:
  - id: condition/25g04/two-graphs
    name: Reading a second graph raises accessing multiple graphs not supported
    kind: condition
    conditions: ["25G04"]
    subclauses: ["8.1"]
    unless: GT03
    query: |
      USE other MATCH (n) RETURN COUNT(*) AS n
    expect:
      kind: error
      gqlstatus: "25G04"
`

func TestAConditionTheEngineHasTheFeatureForIsUnreachableNotUntested(t *testing.T) {
	data := make(map[fixture.Capability]bool, len(fixture.AllCapabilities))
	for _, c := range fixture.AllCapabilities {
		data[c] = true
	}
	d := fake.New(fake.Config{Capabilities: adapter.Capabilities{Data: data, GQLStatus: true}})
	std, err := gqlcompat.LoadFS(fstest.MapFS{"unless.yaml": &fstest.MapFile{Data: []byte(unlessCorpus)}})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	rep, err := std.Run(t.Context(), d, runner.Config{Repeats: 1, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	r := &rep.Cases[0]
	if r.Outcome != runner.Skip || r.Skip != runner.SkipFeaturePresent {
		t.Fatalf("outcome %s skip %q, want a feature-present skip", r.Outcome, r.Skip)
	}
	if !strings.Contains(r.Reason, "GT03") {
		t.Errorf("the reason should name the feature that makes the code unreachable, got %q", r.Reason)
	}
	// The coverage row is what a reader looks at, and a code nothing can raise
	// on this engine has to be told apart from one the run forgot to ask about.
	st := rep.Coverage.Conditions["25G04"]
	if st.Skip != 1 || st.Unreachable != 1 {
		t.Errorf("coverage says skip %d unreachable %d, want 1 and 1", st.Skip, st.Unreachable)
	}
}

// The other half of the same claim: an engine that does raise the code is
// judged on it exactly as before, so unless is an excuse for acceptance and
// never a licence to skip.
func TestAConditionTheEngineDoesRaiseIsStillJudged(t *testing.T) {
	data := make(map[fixture.Capability]bool, len(fixture.AllCapabilities))
	for _, c := range fixture.AllCapabilities {
		data[c] = true
	}
	d := fake.New(fake.Config{
		Capabilities: adapter.Capabilities{Data: data, GQLStatus: true},
		Respond: func(stmt string) (fake.Answer, bool) {
			if !strings.Contains(stmt, "USE") {
				return fake.Answer{}, false
			}
			return fake.Answer{Failure: &adapter.Failure{
				GQLStatus: "25G04", Message: "one graph at a time"}}, true
		},
	})
	std, err := gqlcompat.LoadFS(fstest.MapFS{"unless.yaml": &fstest.MapFile{Data: []byte(unlessCorpus)}})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	rep, err := std.Run(t.Context(), d, runner.Config{Repeats: 1, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if r := &rep.Cases[0]; r.Outcome != runner.Pass {
		t.Fatalf("outcome %s (%s), want pass", r.Outcome, r.Reason)
	}
	if st := rep.Coverage.Conditions["25G04"]; st.Unreachable != 0 {
		t.Errorf("a code the engine raised is not unreachable, got %d", st.Unreachable)
	}
}
