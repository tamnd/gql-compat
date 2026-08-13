package runner_test

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/adapter/fake"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/grammar"
	"github.com/tamnd/gql-compat/runner"
)

// These tests are about the one rule the grammar walk lives under: a statement
// nobody wrote and nothing cites is a lead, and a lead is not a result. It may
// not enter a total, a pass rate, a coverage denominator or an exit status, and
// the only rejection it may report is the one an engine calls invalid syntax.

const exploreSeed = 4242

// walked is what the runner's walk will write, computed the same way the runner
// computes it. A test that needs to script an engine's opinion of a generated
// statement has to know the statements first, and the seed is what makes that
// possible.
func walked(t *testing.T, count int) []grammar.Statement {
	t.Helper()
	g, err := grammar.Load()
	if err != nil {
		t.Fatalf("parsing the grammar artifact: %v", err)
	}
	gen, err := grammar.NewGenerator(g, exploreSeed, grammar.Options{})
	if err != nil {
		t.Fatalf("preparing the walk: %v", err)
	}
	statements, err := gen.GenerateN(count)
	if err != nil {
		t.Fatalf("walking: %v", err)
	}
	return statements
}

// explore is the config the walk runs under, with the grammar and the promotion
// list left for the Standard to fill in, which is how the CLI asks for it.
func explore(count int) *runner.Explore {
	return &runner.Explore{Seed: exploreSeed, Count: count}
}

// syntaxErrorOn builds an engine that calls every statement containing word a
// syntax error and accepts everything else. It stands in for a parser that
// disagrees with the published grammar about one construct, which is the only
// thing this phase is looking for.
func syntaxErrorOn(t *testing.T, word string) adapter.Driver {
	return engine(t, func(c *fake.Config) {
		c.Respond = func(stmt string) (fake.Answer, bool) {
			if !strings.Contains(stmt, word) {
				return fake.Answer{}, false
			}
			return fake.Answer{Failure: &adapter.Failure{
				GQLStatus: runner.StatusSyntaxError,
				Message:   "invalid input near " + word,
			}}, true
		}
	})
}

func TestTheWalkRunsOnlyWhenItIsAskedFor(t *testing.T) {
	rep := run(t, engine(t, nil), runner.Config{Repeats: 1})
	if rep.Exploration != nil {
		t.Fatal("a run that asked for no walk reported one")
	}
}

// The whole design rests on this: Report.Cases and Report.Exploration are
// different fields, and everything scored is summed from the first.
func TestAGeneratedStatementIsInNoTotalAndNoDenominator(t *testing.T) {
	base := run(t, engine(t, nil), runner.Config{Repeats: 1})
	rep := run(t, engine(t, nil), runner.Config{Repeats: 1, Explore: explore(10)})

	x := rep.Exploration
	if x == nil {
		t.Fatal("a walk of ten statements reported nothing")
	}
	if x.Totals.Cases == 0 {
		t.Fatal("the walk ran no statements")
	}
	if x.Walked != 10 {
		t.Errorf("walked %d statements, asked for 10", x.Walked)
	}
	if x.Distinct > x.Walked {
		t.Errorf("%d distinct statements out of %d", x.Distinct, x.Walked)
	}
	if x.Seed != exploreSeed {
		t.Errorf("the report says seed %d, the run used %d", x.Seed, exploreSeed)
	}
	if x.Coverage.Total != 814 {
		t.Errorf("coverage is over %d productions, want the artifact's 814", x.Coverage.Total)
	}

	got := [5]int{rep.Totals.Cases, rep.Totals.Pass, rep.Totals.Fail, rep.Totals.Skip, rep.Totals.Error}
	want := [5]int{base.Totals.Cases, base.Totals.Pass, base.Totals.Fail, base.Totals.Skip, base.Totals.Error}
	if got != want {
		t.Errorf("the scoreboard moved when the walk ran: cases/pass/fail/skip/error was %v and is %v", want, got)
	}
	if len(rep.Cases) != len(base.Cases) {
		t.Errorf("%d cases with the walk, %d without", len(rep.Cases), len(base.Cases))
	}
	for i := range rep.Cases {
		if rep.Cases[i].Kind == corpus.KindGenerated {
			t.Fatalf("%s is in the scored cases", rep.Cases[i].ID)
		}
	}
	if _, ok := rep.Totals.ByKind[corpus.KindGenerated]; ok {
		t.Error("the generated kind has a row in the scoreboard's own map")
	}
	if len(rep.Coverage.Productions) != len(base.Coverage.Productions) {
		t.Errorf("the walk put %d productions into the coverage denominator",
			len(rep.Coverage.Productions)-len(base.Coverage.Productions))
	}
}

func TestASyntaxErrorOnAWalkedStatementIsALeadAndNotAFailure(t *testing.T) {
	word := strings.Fields(walked(t, 1)[0].Text)[0]
	base := run(t, engine(t, nil), runner.Config{Repeats: 1})
	rep := run(t, syntaxErrorOn(t, word), runner.Config{Repeats: 1, Explore: explore(12)})

	x := rep.Exploration
	if x == nil || len(x.Leads) == 0 {
		t.Fatalf("an engine that refuses every statement containing %q produced no lead", word)
	}
	if rep.Totals.Fail != base.Totals.Fail {
		t.Errorf("failures went from %d to %d because of a lead", base.Totals.Fail, rep.Totals.Fail)
	}

	l := x.Leads[0]
	if l.GQLStatus != runner.StatusSyntaxError {
		t.Errorf("the lead reports GQLSTATUS %q, want %q", l.GQLStatus, runner.StatusSyntaxError)
	}
	if !strings.Contains(l.Reduced, word) {
		t.Errorf("the reduced statement lost the construct the engine objected to: %s", l.Reduced)
	}
	if len(l.Reduced) > len(l.Statement) {
		t.Errorf("reduction grew the statement:\n%s\n%s", l.Statement, l.Reduced)
	}
	// Without the path a reader has the statement and no way to tell which of
	// the 814 productions is in dispute, which is the whole point of reducing.
	if len(l.Path) == 0 {
		t.Error("the lead names no productions")
	}
	if l.Tried == 0 {
		t.Error("the reducer put no candidate to the engine")
	}
	if l.Fingerprint != grammar.Fingerprint(l.Reduced) {
		t.Errorf("the fingerprint %s is not the reduced statement's %s",
			l.Fingerprint, grammar.Fingerprint(l.Reduced))
	}
	if l.Message == "" {
		t.Error("the lead does not carry the engine's own words")
	}
}

// A lead recorded in the promotion list is a lead review has answered. The walk
// is seeded, so without this the same statement would be reported forever.
func TestAReviewedLeadIsNotReportedAgain(t *testing.T) {
	word := strings.Fields(walked(t, 1)[0].Text)[0]
	first := run(t, syntaxErrorOn(t, word), runner.Config{Repeats: 1, Explore: explore(12)})
	if len(first.Exploration.Leads) == 0 {
		t.Fatalf("no lead to promote")
	}

	// Only the fingerprint and a reason, which is what a review that decided
	// there was nothing to write leaves behind.
	var doc strings.Builder
	doc.WriteString("promoted:\n")
	for _, l := range first.Exploration.Leads {
		doc.WriteString("  - fingerprint: " + l.Fingerprint + "\n")
		doc.WriteString("    note: the engine documents this restriction under 24.5.3\n")
	}
	promoted, err := grammar.ParsePromoted([]byte(doc.String()), "promoted.yaml")
	if err != nil {
		t.Fatalf("the promotion list this run produced does not load: %v", err)
	}

	cfg := explore(12)
	cfg.Promoted = promoted
	second := run(t, syntaxErrorOn(t, word), runner.Config{Repeats: 1, Explore: cfg})

	for _, l := range second.Exploration.Leads {
		if promoted.Has(l.Reduced) {
			t.Errorf("lead %s was reported again after review dealt with it", l.Fingerprint)
		}
	}
	if second.Exploration.Known == 0 {
		t.Error("the report does not say that anything was already reviewed")
	}
	if len(second.Exploration.Leads) >= len(first.Exploration.Leads) {
		t.Errorf("%d leads after promoting %d of %d",
			len(second.Exploration.Leads), len(first.Exploration.Leads), len(first.Exploration.Leads))
	}
}

// The grammar is syntax and nothing else, so a statement it admits can still be
// meaningless. An engine refusing one of those is right, and a harness that
// counted it would manufacture leads out of its own generator.
func TestARefusalThatIsNotAboutSyntaxIsNotALead(t *testing.T) {
	d := engine(t, func(c *fake.Config) {
		c.Respond = func(string) (fake.Answer, bool) {
			return fake.Answer{Failure: &adapter.Failure{
				GQLStatus: "42002", Message: "unknown variable",
			}}, true
		}
	})
	rep := run(t, d, runner.Config{Repeats: 1, Explore: explore(8)})
	x := rep.Exploration
	if len(x.Leads) != 0 {
		t.Fatalf("%d leads from an engine that never mentioned syntax", len(x.Leads))
	}
	if x.Totals.Fail != 0 {
		t.Errorf("%d of the walk's statements were called failures", x.Totals.Fail)
	}
	if x.Totals.Skip != x.Totals.Cases {
		t.Errorf("%d of %d statements were skipped, want all of them", x.Totals.Skip, x.Totals.Cases)
	}
	for i := range x.Cases {
		if x.Cases[i].Skip != runner.SkipSemantic {
			t.Fatalf("%s was skipped as %q, want %q", x.Cases[i].ID, x.Cases[i].Skip, runner.SkipSemantic)
		}
	}
}

// zu is such an engine, so this is the path the walk takes on one of the three
// engines this project measures. Guessing from the message text is how a
// harness invents findings, and it is not done.
func TestAnEngineWithNoGQLStatusIsNotJudgedOnSyntax(t *testing.T) {
	d := engine(t, func(c *fake.Config) {
		c.Capabilities.GQLStatus = false
		c.Respond = func(string) (fake.Answer, bool) {
			return fake.Answer{Failure: &adapter.Failure{Message: "syntax error at or near 'SESSION'"}}, true
		}
	})
	rep := run(t, d, runner.Config{Repeats: 1, Explore: explore(8)})
	x := rep.Exploration
	if len(x.Leads) != 0 {
		t.Fatalf("%d leads from an engine that reports no GQLSTATUS", len(x.Leads))
	}
	if x.Totals.Skip != x.Totals.Cases {
		t.Errorf("%d of %d statements were skipped, want all of them", x.Totals.Skip, x.Totals.Cases)
	}
	for i := range x.Cases {
		if x.Cases[i].Skip != runner.SkipNoGQLStatus {
			t.Errorf("%s was skipped as %q, want %q", x.Cases[i].ID, x.Cases[i].Skip, runner.SkipNoGQLStatus)
		}
	}
}
