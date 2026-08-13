package runner_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	gqlcompat "github.com/tamnd/gql-compat"
	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/adapter/fake"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/iso"
	"github.com/tamnd/gql-compat/rows"
	"github.com/tamnd/gql-compat/runner"
)

// These tests are about the harness's judgement, not about any database. What
// has to be true is that a correct answer passes, a wrong one fails, a missing
// capability skips rather than fails, and the difference between an engine
// that names the right GQLSTATUS and one that merely refuses survives into the
// report. None of that can be established against a real engine, because
// against a real engine the answer is the unknown.

// miniCorpus is a corpus small enough to reason about completely. It is
// written as a file rather than built as structs so that it goes through the
// same loader and the same ISO validation the shipped corpus does.
const miniCorpus = `
fixtures:
  - name: two
    description: Two people who know each other
    nodes:
      - {key: a, labels: [Person], props: {name: Ada, age: 36}}
      - {key: b, labels: [Person], props: {name: Bob, age: 41}}
    edges:
      - {type: KNOWS, from: a, to: b}
  - name: dated
    description: A node with a temporal property
    nodes:
      - {key: d, labels: [Event], props: {on: {date: "2024-01-15"}}}

cases:
  - id: mandatory/test/right-answer
    name: A correct table passes
    kind: mandatory
    subclauses: ["14.4"]
    fixture: two
    query: MATCH (p:Person) RETURN p.name AS name
    expect:
      kind: rows
      unordered: true
      columns: [name]
      rows: [[Ada], [Bob]]

  - id: mandatory/test/wrong-answer
    name: A wrong table fails
    kind: mandatory
    subclauses: ["14.4"]
    fixture: two
    query: MATCH (p:Person) RETURN p.age AS age
    expect:
      kind: rows
      unordered: true
      columns: [age]
      rows: [[36], [41]]

  - id: condition/22012/divide
    name: Division by zero names a condition
    kind: condition
    conditions: ["22012"]
    subclauses: ["20.21"]
    query: RETURN 1 / 0 AS v
    expect:
      kind: error
      gqlstatus: "22012"

  - id: mandatory/test/temporal
    name: A fixture the engine cannot hold is skipped
    kind: mandatory
    subclauses: ["21.2"]
    fixture: dated
    query: MATCH (e:Event) RETURN e.on AS on
    expect:
      kind: rows
      columns: [on]
      rows: [["2024-01-15"]]

  - id: optional/gq13/needs-limit
    name: A case whose requirement the engine declines is skipped
    kind: optional
    features: [GQ13]
    requires: [GQ13]
    fixture: two
    query: MATCH (p:Person) RETURN p.name AS name LIMIT 1
    expect:
      kind: rows
      columns: [name]
      rows: [[Ada]]

  - id: condition/08007/unreachable
    name: A condition no client can provoke is withdrawn before the engine sees it
    kind: condition
    conditions: ["08007"]
    subclauses: ["8.4"]
    query: COMMIT
    unprovokable: >-
      the connection has to die between the commit and its answer, and nothing
      a driver can send arranges that
    expect:
      kind: error
      gqlstatus: "08007"

  - id: mandatory/test/write
    name: A mutating case is never warmed up
    kind: mandatory
    subclauses: ["13.2"]
    fixture: two
    mutating: true
    query: |
      INSERT (:Person {name: 'Cy'})
    expect:
      kind: accept
`

func load(t *testing.T) *gqlcompat.Standard {
	t.Helper()
	std, err := gqlcompat.LoadFS(fstest.MapFS{"mini.yaml": &fstest.MapFile{Data: []byte(miniCorpus)}})
	if err != nil {
		t.Fatalf("loading the test corpus: %v", err)
	}
	return std
}

// table is the shorthand the scripted answers are written in.
func table(col string, values ...any) *rows.Table {
	t := &rows.Table{Columns: []string{col}}
	for _, v := range values {
		t.Rows = append(t.Rows, []any{v})
	}
	return t
}

// engine builds a fake with the answers a mostly-correct database would give:
// right on the first case, wrong on the second, an error with the right status
// on the third.
func engine(t *testing.T, adjust func(*fake.Config)) adapter.Driver {
	t.Helper()
	// Every capability is named, including the ones this engine does not have.
	// The runner refuses a map with a hole in it, because a hole reads as "no"
	// and there is no way afterwards to tell a limitation from an oversight.
	supported := map[fixture.Capability]bool{
		fixture.CapLabels:            true,
		fixture.CapNodeProperties:    true,
		fixture.CapEdgeTypes:         true,
		fixture.CapMultipleEdgeTypes: true,
	}
	data := make(map[fixture.Capability]bool, len(fixture.AllCapabilities))
	for _, c := range fixture.AllCapabilities {
		data[c] = supported[c]
	}
	caps := adapter.Capabilities{Data: data, GQLStatus: true}
	cfg := fake.Config{
		Capabilities: caps,
		Answers: map[string]fake.Answer{
			"MATCH (p:Person) RETURN p.name AS name": {Table: table("name", "Ada", "Bob")},
			"MATCH (p:Person) RETURN p.age AS age":   {Table: table("age", 36, 999)},
			"RETURN 1 / 0 AS v": {Failure: &adapter.Failure{
				GQLStatus: "22012", Message: "division by zero"}},
		},
		BytesPerNode: 64,
	}
	if adjust != nil {
		adjust(&cfg)
	}
	return fake.New(cfg)
}

func run(t *testing.T, d adapter.Driver, cfg runner.Config) *runner.Report {
	t.Helper()
	if cfg.Repeats == 0 {
		cfg.Repeats = 2
	}
	cfg.WorkDir = t.TempDir()
	rep, err := load(t).Run(t.Context(), d, cfg)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	return rep
}

func result(t *testing.T, rep *runner.Report, id string) *runner.CaseResult {
	t.Helper()
	for i := range rep.Cases {
		if rep.Cases[i].ID == id {
			return &rep.Cases[i]
		}
	}
	t.Fatalf("no result for %s", id)
	return nil
}

// The Neo4j adapter shipped with float-values and boolean-values missing from
// its map and the run of 2026-08-12 published "no" against an engine that
// supports both, skipping four cases to get there. A missing key and a key set
// to false are the same value in Go and they are not the same claim, so the
// run stops before it can print one as the other.
func TestAnUndeclaredCapabilityStopsTheRunRatherThanReadingAsNo(t *testing.T) {
	d := engine(t, func(c *fake.Config) {
		delete(c.Capabilities.Data, fixture.CapFloatValues)
	})
	_, err := load(t).Run(t.Context(), d, runner.Config{Repeats: 1, WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("a run started against an adapter that never mentioned float-values")
	}
	if !strings.Contains(err.Error(), string(fixture.CapFloatValues)) {
		t.Errorf("error %q does not name the capability that was left out", err)
	}
}

func TestCorrectAnswerPasses(t *testing.T) {
	rep := run(t, engine(t, nil), runner.Config{})
	r := result(t, rep, "mandatory/test/right-answer")
	if r.Outcome != runner.Pass {
		t.Fatalf("outcome %s: %s", r.Outcome, r.Reason)
	}
	if r.Evidence != runner.EvidenceRows {
		t.Errorf("evidence %q, want rows", r.Evidence)
	}
}

func TestWrongAnswerFailsAndSaysWhere(t *testing.T) {
	rep := run(t, engine(t, nil), runner.Config{})
	r := result(t, rep, "mandatory/test/wrong-answer")
	if r.Outcome != runner.Fail {
		t.Fatalf("outcome %s, want fail", r.Outcome)
	}
	// A failure whose diff does not locate the difference sends a reader back
	// to rerun the case by hand, which is the thing the report exists to avoid.
	if r.Diff == nil {
		t.Fatal("a row mismatch produced no diff")
	}
	if !strings.Contains(r.Diff.Got, "999") && !strings.Contains(r.Diff.Want, "41") {
		t.Errorf("the diff names neither value: want %q got %q", r.Diff.Want, r.Diff.Got)
	}
}

func TestRightStatusIsStrongerEvidenceThanRefusal(t *testing.T) {
	// With GQLSTATUS reporting the engine's 22012 is checked against the
	// corpus's 22012 and the pass is recorded as resting on the code.
	rep := run(t, engine(t, nil), runner.Config{})
	r := result(t, rep, "condition/22012/divide")
	if r.Outcome != runner.Pass || r.Evidence != runner.EvidenceStatus {
		t.Fatalf("outcome %s evidence %q, want pass on gqlstatus", r.Outcome, r.Evidence)
	}

	// The same refusal from an engine that reports no status is not evidence
	// at all, and must not be scored as one.
	//
	// This was a pass on weak evidence until a real run made the consequence
	// plain: zu reports no GQLSTATUS, refused all twelve condition cases it
	// was asked, and the coverage table printed codes as `supported` on the
	// strength of refusals that had nothing to do with the condition —
	// 22G0M, multiple assignments to one property, "passed" because the
	// engine's SET is unimplemented. Every engine can refuse a statement, so
	// a bare refusal distinguishes no engine from any other.
	silent := engine(t, func(c *fake.Config) {
		c.Capabilities.GQLStatus = false
		c.Answers["RETURN 1 / 0 AS v"] = fake.Answer{Failure: &adapter.Failure{Message: "cannot divide"}}
	})
	rep = run(t, silent, runner.Config{})
	r = result(t, rep, "condition/22012/divide")
	if r.Outcome != runner.Skip || r.Skip != runner.SkipNoGQLStatus {
		t.Fatalf("outcome %s skip %q, want a no-gqlstatus skip", r.Outcome, r.Skip)
	}
	if !strings.Contains(r.Reason, "22012") {
		t.Errorf("the skip must name the code it could not verify: %q", r.Reason)
	}
	// A skip is never a pass, so it cannot lift the rate.
	if got := rep.Totals.ByKind["condition"]; got.Pass != 0 || got.Skip == 0 {
		t.Errorf("condition totals %+v: the unverifiable case must be skipped, not passed", got)
	}
}

// TestAnEngineThatPromisesStatusCodesMustProduceOne is the other half of the
// rule above. Declining to report GQLSTATUS is lawful — GB01 is optional — but
// declaring the capability and then refusing a specified condition without a
// code is a failure of the case, not an absence of evidence.
func TestAnEngineThatPromisesStatusCodesMustProduceOne(t *testing.T) {
	mute := engine(t, func(c *fake.Config) {
		c.Answers["RETURN 1 / 0 AS v"] = fake.Answer{Failure: &adapter.Failure{Message: "cannot divide"}}
	})
	r := result(t, run(t, mute, runner.Config{}), "condition/22012/divide")
	if r.Outcome != runner.Fail {
		t.Fatalf("outcome %s, want fail", r.Outcome)
	}
	if !strings.Contains(r.Reason, "22012") {
		t.Errorf("the failure must name the code the standard specifies: %q", r.Reason)
	}
}

func TestWrongStatusFailsAndNamesBoth(t *testing.T) {
	wrong := engine(t, func(c *fake.Config) {
		c.Answers["RETURN 1 / 0 AS v"] = fake.Answer{Failure: &adapter.Failure{
			GQLStatus: "42001", Message: "syntax error"}}
	})
	r := result(t, run(t, wrong, runner.Config{}), "condition/22012/divide")
	if r.Outcome != runner.Fail {
		t.Fatalf("outcome %s, want fail", r.Outcome)
	}
	if !strings.Contains(r.Reason, "42001") || !strings.Contains(r.Reason, "22012") {
		t.Errorf("the reason should name both codes, got %q", r.Reason)
	}
}

func TestMissingCapabilitySkipsRatherThanFails(t *testing.T) {
	// The engine declared no temporal values, so the dated fixture cannot be
	// loaded. Failing the case would report a conformance defect for a data
	// model choice the case was not about.
	r := result(t, run(t, engine(t, nil), runner.Config{}), "mandatory/test/temporal")
	if r.Outcome != runner.Skip {
		t.Fatalf("outcome %s, want skip", r.Outcome)
	}
	if r.Skip != runner.SkipCapability {
		t.Errorf("skip reason %q, want %q", r.Skip, runner.SkipCapability)
	}
	if len(r.Missing) == 0 {
		t.Error("a capability skip must name the capability it lacked")
	}
}

func TestDeclaredUnsupportedFeatureSkipsARequiringCase(t *testing.T) {
	declined := engine(t, func(c *fake.Config) {
		c.Capabilities.Unsupported = []string{"GQ13"}
	})
	r := result(t, run(t, declined, runner.Config{}), "optional/gq13/needs-limit")
	if r.Outcome != runner.Skip || r.Skip != runner.SkipRequires {
		t.Fatalf("outcome %s skip %q, want a required-feature skip", r.Outcome, r.Skip)
	}
	if !strings.Contains(r.Reason, "GQ13") {
		t.Errorf("the reason should name the feature, got %q", r.Reason)
	}
}

func TestAnUnprovokableConditionNeverReachesTheEngine(t *testing.T) {
	// Two of ISO's codes are raised by the connection failing, and the corpus
	// carries a case for each so that the condition surface has no silent gaps.
	// What must not happen is the statement being sent anyway: COMMIT succeeds
	// against an ordinary engine, and a success on a case expecting 08007 would
	// be recorded as a failure to raise a condition nobody could have raised.
	commits := 0
	d := countingDriver{
		Driver: engine(t, func(c *fake.Config) {
			c.Answers["COMMIT"] = fake.Answer{Table: &rows.Table{}}
		}),
		count: func(stmt string) {
			if strings.Contains(stmt, "COMMIT") {
				commits++
			}
		},
	}
	r := result(t, run(t, d, runner.Config{}), "condition/08007/unreachable")
	if r.Outcome != runner.Skip || r.Skip != runner.SkipNotProvokable {
		t.Fatalf("outcome %s skip %q, want a not-provokable skip", r.Outcome, r.Skip)
	}
	if !strings.Contains(r.Reason, "connection has to die") {
		t.Errorf("the skip should carry the case's own reason, got %q", r.Reason)
	}
	if r.WantStatus != "08007" {
		t.Errorf("want_gqlstatus %q, want the code the case names", r.WantStatus)
	}
	if commits != 0 {
		t.Errorf("the engine was asked %d statement(s) for a withdrawn case, want 0", commits)
	}
}

// countingDriver watches what a run actually sends. It exists for the one
// guarantee that cannot be read off a result: that a withdrawn case reached no
// engine at all, rather than reaching one and being reclassified afterwards.
type countingDriver struct {
	adapter.Driver
	count func(stmt string)
}

func (d countingDriver) Open(ctx context.Context, workdir string) (adapter.Session, error) {
	s, err := d.Driver.Open(ctx, workdir)
	if err != nil {
		return nil, err
	}
	return countingSession{Session: s, count: d.count}, nil
}

type countingSession struct {
	adapter.Session
	count func(stmt string)
}

func (s countingSession) Exec(ctx context.Context, stmt string, params map[string]any) (*adapter.Result, error) {
	s.count(stmt)
	return s.Session.Exec(ctx, stmt, params)
}

func TestSkippingCannotImproveTheRate(t *testing.T) {
	// The guarantee behind the headline number: a skip leaves the denominator
	// alone. An engine that declines more work scores the same, not better.
	base := run(t, engine(t, nil), runner.Config{})
	declined := run(t, engine(t, func(c *fake.Config) {
		c.Capabilities.Unsupported = []string{"GQ13"}
	}), runner.Config{})

	k1 := base.Totals.ByKind[corpus.KindMandatory]
	k2 := declined.Totals.ByKind[corpus.KindMandatory]
	if k1.Rate() != k2.Rate() {
		t.Errorf("declining a feature changed the mandatory rate from %.3f to %.3f", k1.Rate(), k2.Rate())
	}
	if declined.Totals.Skip <= base.Totals.Skip {
		t.Errorf("the declining engine should have skipped more: %d then %d",
			base.Totals.Skip, declined.Totals.Skip)
	}
}

func TestMutatingCaseIsNotWarmedUp(t *testing.T) {
	// A warm-up of a write is an unmeasured write. It would change what the
	// timed repetitions then measure, and it would leave the graph holding
	// rows nobody accounted for.
	r := result(t, run(t, engine(t, nil), runner.Config{Warmups: 3, Repeats: 2}), "mandatory/test/write")
	if r.Warmups != 0 {
		t.Errorf("the result records %d warmups on a mutating case, want 0", r.Warmups)
	}
	if r.Outcome != runner.Pass {
		t.Fatalf("outcome %s: %s", r.Outcome, r.Reason)
	}

	// A read-only case in the same run does get them, so the zero above is the
	// rule and not a warmup path that is broken for everything.
	if got := result(t, run(t, engine(t, nil), runner.Config{Warmups: 3, Repeats: 2}),
		"mandatory/test/right-answer").Warmups; got != 3 {
		t.Errorf("a read-only case recorded %d warmups, want 3", got)
	}
}

// The plan is the answer to "why was that case slow", and until it was
// recorded the answer meant reproducing the run by hand against a graph the
// harness had already deleted. What it must not do is cost a measurement
// anything, which is why the runner asks the engine to describe the statement
// rather than to run it and count.
func TestThePlanIsRecordedOncePerCaseAndNotOncePerRepetition(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]int{}
	d := engine(t, func(c *fake.Config) {
		c.Explain = func(stmt string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			asked[stmt]++
			return "Project\n  ScanNodes(Person)", nil
		}
	})
	rep := run(t, d, runner.Config{Repeats: 4, Warmups: 2})

	r := result(t, rep, "mandatory/test/right-answer")
	if !strings.Contains(r.Plan, "ScanNodes") {
		t.Fatalf("the case recorded no plan: %q", r.Plan)
	}
	// Four measured executions and two warmups all ran the same text. A plan
	// bought on any of them would show up here as more than one request, and a
	// plan bought inside the timed loop is a latency figure that is measuring
	// the harness.
	mu.Lock()
	defer mu.Unlock()
	if n := asked[strings.TrimSpace(r.Statement)]; n != 1 {
		t.Errorf("the engine was asked for this plan %d times, want 1", n)
	}
}

// An engine that cannot describe a statement without running it must not
// implement Explainer at all, and the harness has to survive that rather than
// printing its own guess in the column.
func TestAnEngineThatCannotExplainLeavesThePlanEmpty(t *testing.T) {
	rep := run(t, engine(t, nil), runner.Config{})
	for i := range rep.Cases {
		if p := rep.Cases[i].Plan; p != "" {
			t.Errorf("%s: an engine with no Explainer produced a plan %q", rep.Cases[i].ID, p)
		}
	}
}

// A statement the engine cannot compile has no plan. Recording the refusal as
// one would put a second error in the report for a case whose outcome column
// already says it did not parse.
func TestAPlanTheEngineRefusesIsDroppedAndChangesNoVerdict(t *testing.T) {
	d := engine(t, func(c *fake.Config) {
		c.Explain = func(string) (string, error) {
			return "", &adapter.Failure{GQLStatus: "42001", Message: "syntax error"}
		}
	})
	rep := run(t, d, runner.Config{})
	r := result(t, rep, "mandatory/test/right-answer")
	if r.Plan != "" {
		t.Errorf("a refused explain was recorded as a plan: %q", r.Plan)
	}
	if r.Outcome != runner.Pass {
		t.Errorf("outcome %s: an engine that would not explain the statement changed the verdict on it: %s",
			r.Outcome, r.Reason)
	}
}

// Every latency in the report has the harness's own round trip in it, and how
// much varies by an order of magnitude between an engine behind a pipe and one
// across a socket. The run measures it so the report can print it, rather than
// leaving a reader to compare two engines' transports and call it a query
// comparison.
func TestTheRoundTripFloorIsMeasuredBeforeAnyCase(t *testing.T) {
	d := engine(t, func(c *fake.Config) {
		c.Answers[runner.FloorStatement] = fake.Answer{
			Table: table("n", 1), Latency: 2 * time.Millisecond,
		}
	})
	rep := run(t, d, runner.Config{Repeats: 3, Warmups: 1})
	rt := rep.Engine.RoundTrip
	if !rt.OK {
		t.Fatalf("no floor was measured: %s", rt.Note)
	}
	if rt.Statement != runner.FloorStatement {
		t.Errorf("the floor was measured with %q", rt.Statement)
	}
	if rt.Stats.Count != 3 {
		t.Errorf("floor over %d samples, want the run's repeat count", rt.Stats.Count)
	}
	if rt.Stats.P50 < time.Millisecond {
		t.Errorf("floor p50 %s, want the 2ms the engine was told to take", rt.Stats.P50)
	}
}

// An engine that will not answer the cheapest statement is a finding, not an
// error. The report then has no floor and has to say so, and every other
// measurement in the run still happens.
func TestAnEngineThatRefusesTheFloorStatementStillGetsAReport(t *testing.T) {
	d := engine(t, func(c *fake.Config) {
		c.Answers[runner.FloorStatement] = fake.Answer{
			Failure: &adapter.Failure{GQLStatus: "42001", Message: "RETURN must follow MATCH"},
		}
	})
	rep := run(t, d, runner.Config{})
	if rep.Engine.RoundTrip.OK {
		t.Fatal("a floor was reported for an engine that refused to be asked")
	}
	if rep.Engine.RoundTrip.Note == "" {
		t.Error("no reason given for the missing floor")
	}
	if r := result(t, rep, "mandatory/test/right-answer"); r.Outcome != runner.Pass {
		t.Errorf("outcome %s: the refused floor statement took the rest of the run with it: %s",
			r.Outcome, r.Reason)
	}
}

func TestEveryCaseIsMeasured(t *testing.T) {
	rep := run(t, engine(t, nil), runner.Config{Repeats: 3})
	for i := range rep.Cases {
		r := &rep.Cases[i]
		if r.Outcome == runner.Skip {
			continue
		}
		if r.Stats.Count == 0 {
			t.Errorf("%s: ran but produced no latency samples", r.ID)
		}
		if r.Wall <= 0 {
			t.Errorf("%s: no wall time", r.ID)
		}
	}
	// The ingest is measured once and attached to the case that caused it, so
	// exactly one case per fixture carries a Load. A report that attached one
	// to every case would let a reader sum them and get a number no run spent.
	loads := 0
	for i := range rep.Cases {
		if rep.Cases[i].Load != nil {
			loads++
		}
	}
	if loads == 0 {
		t.Error("no case recorded a fixture load")
	}
}

func TestCoverageUsesISODenominators(t *testing.T) {
	rep := run(t, engine(t, nil), runner.Config{})
	if rep.Coverage.FeaturesTotal != 228 {
		t.Errorf("features total %d, want ISO's 228", rep.Coverage.FeaturesTotal)
	}
	// The corpus here claims exactly one feature. A denominator taken from the
	// corpus would make that 100% coverage of the standard.
	if len(rep.Coverage.Features) >= rep.Coverage.FeaturesTotal {
		t.Errorf("a six-case corpus claims %d of %d features",
			len(rep.Coverage.Features), rep.Coverage.FeaturesTotal)
	}
}

func TestCompatModeSkipsCasesWithNoDialect(t *testing.T) {
	// No case in the mini corpus has a dialect for this engine, so every one of
	// them that is put to the engine at all is a no-dialect skip. Nobody claimed
	// a spelling exists, so nothing here is a failure. The withdrawn condition
	// case is the exception and stays withdrawn: an engine's dialect cannot make
	// a dead connection reachable, so it is counted out of the denominator here
	// rather than reclassified.
	rep := run(t, engine(t, nil), runner.Config{Mode: runner.ModeCompat})
	if rep.Totals.Fail != 0 {
		t.Errorf("%d failures in compat mode with no dialects declared", rep.Totals.Fail)
	}
	asked := rep.Totals.Cases - rep.Totals.BySkip[runner.SkipNotProvokable]
	if rep.Totals.BySkip[runner.SkipNoDialect] != asked {
		t.Errorf("%d of %d cases skipped for no dialect",
			rep.Totals.BySkip[runner.SkipNoDialect], asked)
	}
}

func TestTimeoutIsAnErrorNotAFailure(t *testing.T) {
	// An engine the harness cut off has not been shown to be wrong about
	// anything. Counting it as a conformance failure would put the harness's
	// own patience into the score.
	slow := engine(t, func(c *fake.Config) {
		c.Answers["MATCH (p:Person) RETURN p.name AS name"] = fake.Answer{
			Table: table("name", "Ada", "Bob"), Latency: 500 * time.Millisecond}
	})
	rep := run(t, slow, runner.Config{Repeats: 1, Timeout: 20 * time.Millisecond})
	r := result(t, rep, "mandatory/test/right-answer")
	if r.Outcome != runner.Error {
		t.Fatalf("outcome %s, want error", r.Outcome)
	}
	if !strings.Contains(r.Reason, "timed out") {
		t.Errorf("reason %q should say it timed out", r.Reason)
	}
}

func TestATimeoutIsAskedOnceEvenWhenTheCaseExpectsARefusal(t *testing.T) {
	// The Ladybug shell treats an unterminated string literal as an incomplete
	// statement and waits for the rest of it, so the two cases that assert such
	// a literal is rejected timed out instead. Both expect a refusal, and a
	// refusal was the outcome the repeat loop was willing to keep collecting,
	// so each of them was asked eight times and spent 240s of a 1138s run. The
	// verdict after the first timeout is already an error and no repetition can
	// change it.
	mute := engine(t, func(c *fake.Config) {
		c.Answers["RETURN 1 / 0 AS v"] = fake.Answer{
			Failure: &adapter.Failure{GQLStatus: "22012", Message: "division by zero"},
			Latency: time.Second,
		}
	})
	rep := run(t, mute, runner.Config{Repeats: 8, Warmups: 4, Timeout: 20 * time.Millisecond})

	r := result(t, rep, "condition/22012/divide")
	if r.Outcome != runner.Error {
		t.Fatalf("outcome %s: %s", r.Outcome, r.Reason)
	}
	if r.Stats.Count != 1 {
		t.Errorf("the case was timed %d times, want 1: a repetition after a timeout costs the whole timeout again", r.Stats.Count)
	}
	// The case's own wall, not the run's, so an instrumented build's overhead
	// on the other cases does not decide this. Twelve executions of a 20ms
	// timeout is 240ms and two is 40ms, so the bound is loose enough for a
	// loaded machine under -race and still far under the old cost.
	if r.Wall > 150*time.Millisecond {
		t.Errorf("the case took %v; a warmup and a repetition at a 20ms timeout should not approach the twelve the config asks for", r.Wall)
	}
}

func TestTransportBreakageIsAnErrorNotAFailure(t *testing.T) {
	// An adapter whose plumbing broke has not learned anything about the
	// engine. The case that provoked this is a shell that computes the right
	// answer and then dies printing it, which as a failure reads as a query
	// the database cannot answer and is simply untrue.
	broken := engine(t, func(c *fake.Config) {
		c.Answers["MATCH (p:Person) RETURN p.name AS name"] = fake.Answer{
			Failure: &adapter.Failure{Transport: true, Message: "the shell's JSON writer died"}}
	})
	rep := run(t, broken, runner.Config{Repeats: 1})
	r := result(t, rep, "mandatory/test/right-answer")
	if r.Outcome != runner.Error {
		t.Fatalf("outcome %s, want error", r.Outcome)
	}
	if !strings.Contains(r.Reason, "JSON writer") {
		t.Errorf("reason %q should carry the adapter's own words", r.Reason)
	}
	// The same suite against the same engine with working plumbing, so that
	// what is compared is the breakage and not the corpus.
	base := run(t, engine(t, nil), runner.Config{Repeats: 1})
	if rep.Totals.Fail != base.Totals.Fail {
		t.Errorf("failures went from %d to %d; broken plumbing must not be charged to the engine",
			base.Totals.Fail, rep.Totals.Fail)
	}
}

func TestAnIngestThatNeverFinishesIsAFindingNotAHang(t *testing.T) {
	// Ladybug spent six minutes on a hundred-thousand-node fixture and was
	// still going, with no bound on a load and therefore no report at the end
	// of it. A load gets its own patience, larger than a statement's, and when
	// it runs out the cases that wanted the fixture say why.
	slow := engine(t, func(c *fake.Config) { c.LoadLatency = time.Minute })
	rep := run(t, slow, runner.Config{Repeats: 1, LoadTimeout: 10 * time.Millisecond})
	var errs int
	for i := range rep.Cases {
		if r := &rep.Cases[i]; r.Outcome == runner.Error && strings.Contains(r.Reason, "did not finish within") {
			errs++
		}
	}
	if errs == 0 {
		t.Fatal("no case reported the fixture that would not load")
	}
	if rep.Totals.Fail != 0 {
		t.Errorf("%d failures; a fixture the harness gave up on is not a wrong answer", rep.Totals.Fail)
	}
	// The second case that wants the same fixture gets the first case's
	// answer, or a suite of a hundred pays the timeout a hundred times.
	if rep.Run.Wall > 5*time.Second {
		t.Errorf("run took %s; a failed load should be attempted once", rep.Run.Wall)
	}
}

// probes builds a probe set of one question, against a graph the mini corpus
// actually holds.
func probes(t *testing.T, p *impdef.Probe) *impdef.Set {
	t.Helper()
	cat, err := iso.Load()
	if err != nil {
		t.Fatal(err)
	}
	set, err := impdef.New([]*impdef.Probe{p}, iso.Codes{Catalog: cat})
	if err != nil {
		t.Fatalf("building the probe set: %v", err)
	}
	return set
}

// TestAnObservationIsInNoTotal. The probes run in the same session as the
// cases and against the same graphs, and none of what they see may reach the
// scoreboard: ISO delegated these choices, so there is no right answer for an
// engine to miss and nothing here for a CI gate to read.
func TestAnObservationIsInNoTotal(t *testing.T) {
	set := probes(t, &impdef.Probe{
		ID: "impdef/ia015/padding", Item: "IA015", Kind: impdef.Defined,
		Question: "Whether 'a' and 'a  ' compare equal.",
		Fixture:  "two", Statement: "RETURN 'a' = 'a  ' AS v", Read: impdef.Cell,
	})
	d := engine(t, func(c *fake.Config) {
		c.Answers["RETURN 'a' = 'a  ' AS v"] = fake.Answer{Table: table("v", false)}
	})
	rep := run(t, d, runner.Config{Repeats: 1, Probes: set})
	base := run(t, engine(t, nil), runner.Config{Repeats: 1, Probes: &impdef.Set{}})

	if rep.Implementation.Len() != 1 {
		t.Fatalf("%d observations, want the one probe that ran", rep.Implementation.Len())
	}
	o := rep.Implementation.Observations[0]
	if !o.Observed() || o.Value != "false" {
		t.Errorf("observed %q, silence %q; want the engine's answer", o.Value, o.Silence)
	}
	if o.Description == "" {
		t.Error("the observation does not carry the standard's own words for IA015")
	}
	if rep.Totals.Cases != base.Totals.Cases || rep.Totals.Pass != base.Totals.Pass {
		t.Errorf("totals moved from %+v to %+v when a probe ran", base.Totals, rep.Totals)
	}
	if len(rep.Cases) != len(base.Cases) {
		t.Error("a probe was counted as a case")
	}
	if got := len(rep.Coverage.Subclauses); got != len(base.Coverage.Subclauses) {
		t.Error("a probe entered a coverage denominator")
	}
}

// TestAProbeThatCannotBeAskedObservesNothing. A caller running their own
// corpus has no obligation to define the graphs the shipped probes want, and
// the honest report of a question nobody could put is silence, not a default.
func TestAProbeThatCannotBeAskedObservesNothing(t *testing.T) {
	set := probes(t, &impdef.Probe{
		ID: "impdef/id022/collation", Item: "ID022", Kind: impdef.Defined,
		Question: "What order strings sort in by default.",
		Fixture:  "no-such-graph", Statement: "RETURN 1 AS v", Read: impdef.Cell,
	})
	rep := run(t, engine(t, nil), runner.Config{Repeats: 1, Probes: set})
	if rep.Implementation.Len() != 1 {
		t.Fatalf("%d observations, want one", rep.Implementation.Len())
	}
	o := rep.Implementation.Observations[0]
	if o.Observed() {
		t.Errorf("a probe whose graph does not exist reported %q as an answer", o.Value)
	}
	if o.Silence != impdef.NoFixture {
		t.Errorf("silence %q, want %q", o.Silence, impdef.NoFixture)
	}
	if o.Display() != "—" {
		t.Errorf("it renders as %q rather than an em dash", o.Display())
	}
	if rep.Totals.Error != 0 {
		t.Error("a probe nobody could ask was counted as an error")
	}
}

func TestRunNeedsADriver(t *testing.T) {
	if _, err := runner.Run(context.Background(), runner.Config{}); err == nil {
		t.Fatal("a run with no driver should not start")
	}
}

// A declaration is a claim, and until this run there was no way to test one.
// An engine that declares a feature it in fact has turns real passes into
// skips, which reads in the report exactly like a limitation and is caught by
// nothing, because the cases that would catch it are the ones that did not
// run. Challenging the declaration runs them.
func TestAChallengedClaimEveryCasePassesIsReportedAsContradicted(t *testing.T) {
	lying := engine(t, func(c *fake.Config) {
		c.Capabilities.Unsupported = []string{"GQ13"}
		c.Answers["MATCH (p:Person) RETURN p.name AS name LIMIT 1"] = fake.Answer{
			Table: table("name", "Ada"),
		}
	})
	rep := run(t, lying, runner.Config{Challenge: true})
	r := result(t, rep, "optional/gq13/needs-limit")
	if r.Outcome != runner.Pass {
		t.Fatalf("outcome %s: the case the declaration excluded was not put to the engine: %s",
			r.Outcome, r.Reason)
	}
	if len(r.Challenges) != 1 || r.Challenges[0].Reason != runner.SkipRequires {
		t.Fatalf("challenges %+v, want one required-feature override", r.Challenges)
	}
	d := declaration(t, rep, "GQ13")
	if !d.Contradicted {
		t.Errorf("%+v: every case for a feature the engine says it lacks passed, and the claim still stands", d)
	}
	if len(d.Passing) == 0 {
		t.Error("a contradiction that names no case cannot be reproduced")
	}
}

// The ordinary outcome, and the reason the declaration is believed by default:
// the engine said it could not, and it could not. A run that reported this as
// a finding would report one for every honest declaration in the file.
func TestAChallengedClaimTheCasesFailIsLeftStanding(t *testing.T) {
	rep := run(t, engine(t, func(c *fake.Config) {
		c.Capabilities.Unsupported = []string{"GQ13"}
	}), runner.Config{Challenge: true})
	d := declaration(t, rep, "GQ13")
	if d.Contradicted {
		t.Errorf("%+v: a claim whose cases did not pass was called contradicted", d)
	}
	if d.Cases == 0 {
		t.Error("the claim was never challenged at all")
	}
}

// A fixture capability is challenged through the load rather than through the
// statement, so it is worth its own case: the engine that said it cannot hold
// temporal values is asked to hold some.
func TestChallengingAFixtureCapabilityLoadsTheFixtureAnyway(t *testing.T) {
	rep := run(t, engine(t, nil), runner.Config{Challenge: true})
	r := result(t, rep, "mandatory/test/temporal")
	if r.Outcome == runner.Skip {
		t.Fatalf("the fixture the engine declared it cannot hold was still skipped: %s", r.Reason)
	}
	if len(r.Challenges) == 0 || r.Challenges[0].Reason != runner.SkipCapability {
		t.Fatalf("challenges %+v, want a fixture-capability override", r.Challenges)
	}
	if declaration(t, rep, "temporal-values").Cases == 0 {
		t.Error("the temporal-values claim was not counted")
	}
}

// The default is unchanged, and has to be: a challenging run puts statements to
// an engine that said it could not take them, so its totals are not a score and
// no ordinary run should produce them by accident.
func TestARunThatDoesNotChallengeRecordsNoDeclarations(t *testing.T) {
	rep := run(t, engine(t, func(c *fake.Config) {
		c.Capabilities.Unsupported = []string{"GQ13"}
	}), runner.Config{})
	if rep.Declarations != nil {
		t.Errorf("declarations %+v on a run that believed the declaration", rep.Declarations)
	}
	if rep.Run.Challenge {
		t.Error("the report says the declaration was challenged when it was not")
	}
	r := result(t, rep, "optional/gq13/needs-limit")
	if r.Outcome != runner.Skip || len(r.Challenges) != 0 {
		t.Errorf("outcome %s challenges %+v, want the ordinary skip", r.Outcome, r.Challenges)
	}
}

func declaration(t *testing.T, rep *runner.Report, claim string) runner.DeclarationCheck {
	t.Helper()
	for _, d := range rep.Declarations {
		if d.Claim == claim {
			return d
		}
	}
	t.Fatalf("no declaration check for %q in %+v", claim, rep.Declarations)
	return runner.DeclarationCheck{}
}

// The other half of the timeout report, and a bug the first challenging run
// turned up: an engine that refuses a fixture outright is not an engine that
// ran out of patience, and saying so sends a reader looking for a slow ingest
// that never happened.
func TestAnEngineThatRefusesAFixtureIsNotReportedAsATimeout(t *testing.T) {
	refuses := engine(t, func(c *fake.Config) {
		c.LoadFails = func(string) error { return errors.New("this store holds no dates") }
	})
	rep := run(t, refuses, runner.Config{Repeats: 1})
	var saw int
	for i := range rep.Cases {
		r := &rep.Cases[i]
		if r.Outcome != runner.Error {
			continue
		}
		saw++
		if strings.Contains(r.Reason, "did not finish within") {
			t.Errorf("%s: a load that failed at once was reported as a timeout: %s", r.ID, r.Reason)
		}
		if !strings.Contains(r.Reason, "holds no dates") {
			t.Errorf("%s: the engine's own words are missing from %q", r.ID, r.Reason)
		}
	}
	if saw == 0 {
		t.Fatal("no case reported the fixture the engine refused")
	}
}
