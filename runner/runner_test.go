package runner_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	gqlcompat "github.com/tamnd/gql-compat"
	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/adapter/fake"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/fixture"
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
	caps := adapter.Capabilities{
		Data: map[fixture.Capability]bool{
			fixture.CapLabels:            true,
			fixture.CapNodeProperties:    true,
			fixture.CapEdgeTypes:         true,
			fixture.CapMultipleEdgeTypes: true,
		},
		GQLStatus: true,
	}
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
	// them is a no-dialect skip. Nobody claimed a spelling exists, so nothing
	// here is a failure.
	rep := run(t, engine(t, nil), runner.Config{Mode: runner.ModeCompat})
	if rep.Totals.Fail != 0 {
		t.Errorf("%d failures in compat mode with no dialects declared", rep.Totals.Fail)
	}
	if rep.Totals.BySkip[runner.SkipNoDialect] != rep.Totals.Cases {
		t.Errorf("%d of %d cases skipped for no dialect",
			rep.Totals.BySkip[runner.SkipNoDialect], rep.Totals.Cases)
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

func TestRunNeedsADriver(t *testing.T) {
	if _, err := runner.Run(context.Background(), runner.Config{}); err == nil {
		t.Fatal("a run with no driver should not start")
	}
}
