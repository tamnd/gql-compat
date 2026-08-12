package consensus_test

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/consensus"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/runner"
)

// rep builds a report for one engine from a case id to outcome map, which is
// all these tests care about.
func rep(adapter string, outcomes map[string]runner.Outcome) *runner.Report {
	r := &runner.Report{}
	r.Engine.Adapter = adapter
	r.Engine.Version = "1.0"
	r.Run.Mode = runner.ModeConformance
	for id, o := range outcomes {
		r.Cases = append(r.Cases, runner.CaseResult{
			ID: id, Name: "case " + id, Kind: corpus.KindMandatory,
			Outcome: o, Reason: string(o) + " on " + adapter,
		})
	}
	return r
}

func TestACaseEveryEngineFailsIsQueuedAndOneEngineFailingIsNot(t *testing.T) {
	res, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail, "b": runner.Fail, "c": runner.Pass}),
		rep("beta", map[string]runner.Outcome{"a": runner.Fail, "b": runner.Pass, "c": runner.Pass}),
		rep("gamma", map[string]runner.Outcome{"a": runner.Fail, "b": runner.Pass, "c": runner.Pass}),
	}, []string{"a.json", "b.json", "c.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Review) != 1 || res.Review[0].ID != "a" {
		t.Fatalf("queue = %v, want just case a", ids(res.Review))
	}
	if res.Thin {
		t.Error("three engines should not be a thin comparison")
	}
	a := res.Summarize()
	if a.AllPassed != 1 || a.AllFailed != 1 || a.Divided != 1 {
		t.Errorf("agreement = %+v, want one of each", a)
	}
}

// A skip is not agreement. An engine that could not run a case did not judge
// it, and a case two engines skipped and one failed must not read as three
// engines agreeing.
func TestASkipIsNotAgreement(t *testing.T) {
	res, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
		rep("beta", map[string]runner.Outcome{"a": runner.Skip}),
		rep("gamma", map[string]runner.Outcome{"a": runner.Skip}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Review) != 0 {
		t.Fatalf("queue = %v, want empty: only one engine judged the case", ids(res.Review))
	}
	if got := res.Cases[0].Judged(); got != 1 {
		t.Errorf("judged = %d, want 1", got)
	}
	if a := res.Summarize(); a.Unjudged != 1 {
		t.Errorf("unjudged = %d, want 1", a.Unjudged)
	}
}

// An error is not agreement either: the engine never got an answer to
// disagree with.
func TestAnErrorIsNotAgreement(t *testing.T) {
	res, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
		rep("beta", map[string]runner.Outcome{"a": runner.Fail}),
		rep("gamma", map[string]runner.Outcome{"a": runner.Error}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Review) != 1 {
		t.Fatalf("queue = %v, want case a on two failures", ids(res.Review))
	}
	c := res.Review[0]
	if len(c.Failed) != 2 || len(c.Errored) != 1 {
		t.Errorf("case = %d failed, %d errored, want 2 and 1", len(c.Failed), len(c.Errored))
	}
}

func TestTwoEnginesIsThinAndSaysSo(t *testing.T) {
	res, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
		rep("beta", map[string]runner.Outcome{"a": runner.Fail}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Thin {
		t.Fatal("two engines must be a thin comparison")
	}
	var md strings.Builder
	if err := consensus.WriteMarkdown(&md, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.String(), "coin flip") {
		t.Error("a two-engine report must say the agreement is a coin flip")
	}
}

func TestOneReportIsRefusedAndSoIsAnEngineComparedWithItself(t *testing.T) {
	if _, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
	}, nil); err == nil {
		t.Error("one report should be refused")
	}
	_, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
	}, []string{"one.json", "two.json"})
	if err == nil || !strings.Contains(err.Error(), "agreeing with itself") {
		t.Errorf("err = %v, want a refusal to compare an engine with itself", err)
	}
}

// The rhetoric is the safeguard, so it is tested like any other behaviour: the
// queue must never be presented as an engine finding, and the page must say
// that nothing in it moves a score.
func TestTheQueueIsNeverPresentedAsAnEngineFinding(t *testing.T) {
	res, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
		rep("beta", map[string]runner.Outcome{"a": runner.Fail}),
		rep("gamma", map[string]runner.Outcome{"a": runner.Fail}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var md strings.Builder
	if err := consensus.WriteMarkdown(&md, res); err != nil {
		t.Fatal(err)
	}
	out := md.String()
	for _, want := range []string{
		"corpus review queue",
		"Nothing here changes any pass rate",
		"smoke detector, not a proof",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown is missing %q", want)
		}
	}
	// The heading is the first thing a reader sees, and it has to frame the
	// list correctly before any case id appears.
	head := out[:strings.Index(out, "## What was compared")]
	if !strings.Contains(head, "Corpus review queue") {
		t.Errorf("first heading = %q, want it to name a corpus review queue", strings.TrimSpace(head))
	}
	var text strings.Builder
	if err := consensus.WriteText(&text, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "not an engine finding") {
		t.Error("the terminal form must say the queue is not an engine finding")
	}
}

func TestDispositionsAttachAndStaleOnesAreNamed(t *testing.T) {
	res, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail, "b": runner.Pass}),
		rep("beta", map[string]runner.Outcome{"a": runner.Fail, "b": runner.Pass}),
		rep("gamma", map[string]runner.Outcome{"a": runner.Fail, "b": runner.Pass}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := consensus.ReadDispositions(strings.NewReader(`
version: 1
dispositions:
  - case: a
    verdict: corpus-bug
    note: the expected row count was copied from the wrong fixture
    rule: "16"
  - case: gone
    verdict: shared-gap
    note: nobody implements it
`))
	if err != nil {
		t.Fatal(err)
	}
	res.Dispositions = ds
	if got := len(res.Undisposed()); got != 0 {
		t.Errorf("undisposed = %d, want 0", got)
	}
	stale := res.Stale()
	if len(stale) != 1 || stale[0] != "gone" {
		t.Errorf("stale = %v, want [gone]", stale)
	}
	if got := res.ByVerdict()[consensus.CorpusBug]; len(got) != 1 {
		t.Errorf("corpus-bug cases = %d, want 1", len(got))
	}
}

func TestADispositionWithoutAReasonOrWithAnInventedVerdictIsRefused(t *testing.T) {
	for name, in := range map[string]string{
		"no note":         "version: 1\ndispositions:\n  - case: a\n    verdict: corpus-bug\n",
		"unknown verdict": "version: 1\ndispositions:\n  - case: a\n    verdict: looks-fine\n    note: it does\n",
		"no case":         "version: 1\ndispositions:\n  - verdict: corpus-bug\n    note: which one\n",
		"twice":           "version: 1\ndispositions:\n  - case: a\n    verdict: corpus-bug\n    note: one\n  - case: a\n    verdict: shared-gap\n    note: two\n",
		"future version":  "version: 9\ndispositions: []\n",
	} {
		if _, err := consensus.ReadDispositions(strings.NewReader(in)); err == nil {
			t.Errorf("%s: loaded, want a refusal", name)
		}
	}
	// An empty file is a queue nobody has worked yet, which is legitimate.
	if got, err := consensus.ReadDispositions(strings.NewReader("")); err != nil || len(got) != 0 {
		t.Errorf("empty file: got %v, %v; want an empty set and no error", got, err)
	}
}

// The template is what a reviewer starts from, so it must not load: a
// skeleton that parses would let somebody commit blank decisions.
func TestTheTemplateDoesNotLoadUntilItIsFilledIn(t *testing.T) {
	res, err := consensus.Compare([]*runner.Report{
		rep("alpha", map[string]runner.Outcome{"a": runner.Fail}),
		rep("beta", map[string]runner.Outcome{"a": runner.Fail}),
		rep("gamma", map[string]runner.Outcome{"a": runner.Fail}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tpl := res.Template()
	if !strings.Contains(tpl, "case: a") {
		t.Fatalf("template does not mention the queued case:\n%s", tpl)
	}
	if _, err := consensus.ReadDispositions(strings.NewReader(tpl)); err == nil {
		t.Error("the blank template loaded; a half-filled file must be refused")
	}
}

func TestMixedModesAreFlagged(t *testing.T) {
	a := rep("alpha", map[string]runner.Outcome{"a": runner.Fail})
	b := rep("beta", map[string]runner.Outcome{"a": runner.Fail})
	b.Run.Mode = runner.ModeCompat
	res, err := consensus.Compare([]*runner.Report{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.MixedModes() {
		t.Fatal("two modes should be reported as mixed")
	}
	var md strings.Builder
	if err := consensus.WriteMarkdown(&md, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.String(), "different questions") {
		t.Error("a mixed-mode comparison must say the reports answer different questions")
	}
}

// A reason with a pipe in it would break the markdown table it lands in, and
// the reasons come from engines, which put anything in them.
func TestAReasonCannotBreakTheTable(t *testing.T) {
	a := rep("alpha", map[string]runner.Outcome{"a": runner.Fail})
	a.Cases[0].Reason = "want a|b, got\nc|d"
	b := rep("beta", map[string]runner.Outcome{"a": runner.Fail})
	res, err := consensus.Compare([]*runner.Report{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var md strings.Builder
	if err := consensus.WriteMarkdown(&md, res); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(md.String(), "\n") {
		if strings.Contains(line, "want a") {
			if strings.Contains(line, "\n") || !strings.Contains(line, `a\|b`) {
				t.Errorf("row = %q, want the pipe escaped and the newline folded", line)
			}
		}
	}
}

func ids(cs []*consensus.Case) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}
