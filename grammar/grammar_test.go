package grammar_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/grammar"
)

// These tests are about the walk, not about any engine. What has to be true is
// that the same seed writes the same statements everywhere, that a statement
// the walk wrote is made of productions the artifact defines, that the reducer
// only ever offers statements the grammar still admits, and that nothing here
// can present itself to the rest of the harness as a conformance result.

func load(t *testing.T) *grammar.Grammar {
	t.Helper()
	g, err := grammar.Load()
	if err != nil {
		t.Fatalf("parsing the grammar artifact: %v", err)
	}
	return g
}

func generator(t *testing.T, seed uint64, opt grammar.Options) *grammar.Generator {
	t.Helper()
	gen, err := grammar.NewGenerator(load(t), seed, opt)
	if err != nil {
		t.Fatalf("preparing the walk: %v", err)
	}
	return gen
}

// The counts are the ones the ISO artifact publishes. They are pinned because
// every denominator in the report is one of them, and an artifact that changed
// under the project should stop a build rather than quietly move a percentage.
func TestTheArtifactParsesToTheProductionsISOPublishes(t *testing.T) {
	g := load(t)
	if g.Len() != 814 {
		t.Errorf("%d productions, want 814", g.Len())
	}
	if n := len(g.ProseRules()); n != 23 {
		t.Errorf("%d productions defined in prose, want 23", n)
	}
	// The root of a program has to be there or there is nothing to walk.
	if _, ok := g.Rule(grammar.DefaultStart); !ok {
		t.Fatalf("the artifact defines no <%s>", grammar.DefaultStart)
	}
}

// Every prose production the walk can reach needs a token, or the branch
// through it is dead. This is the check that keeps leaves.go honest as the
// walk's reach grows.
func TestEveryCutNamesAProductionTheArtifactDefines(t *testing.T) {
	g := load(t)
	for _, leaf := range grammar.Leaves() {
		if _, ok := g.Rule(leaf.Rule); !ok {
			t.Errorf("leaves.go cuts <%s>, which the artifact does not define", leaf.Rule)
		}
	}
}

func TestTheSameSeedWritesTheSameStatements(t *testing.T) {
	const seed = 20240401
	first, err := generator(t, seed, grammar.Options{}).GenerateN(25)
	if err != nil {
		t.Fatalf("walking: %v", err)
	}
	second, err := generator(t, seed, grammar.Options{}).GenerateN(25)
	if err != nil {
		t.Fatalf("walking again: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("%d statements then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Text != second[i].Text {
			t.Fatalf("statement %d differs between two walks of the same seed:\n%s\n%s",
				i, first[i].Text, second[i].Text)
		}
		if first[i].ID() != second[i].ID() {
			t.Errorf("statement %d is named %s then %s", i, first[i].ID(), second[i].ID())
		}
	}

	// A different seed that produced the same statements would make the seed
	// decorative and the walk one fixed list.
	other, err := generator(t, seed+1, grammar.Options{}).GenerateN(25)
	if err != nil {
		t.Fatalf("walking a second seed: %v", err)
	}
	same := 0
	for i := range other {
		if other[i].Text == first[i].Text {
			same++
		}
	}
	if same == len(first) {
		t.Error("two seeds wrote the same 25 statements")
	}
}

func TestAWalkedStatementCarriesTheProductionsItUsed(t *testing.T) {
	g := load(t)
	gen, err := grammar.NewGenerator(g, 7, grammar.Options{MinTokens: 6})
	if err != nil {
		t.Fatalf("preparing the walk: %v", err)
	}
	statements, err := gen.GenerateN(40)
	if err != nil {
		t.Fatalf("walking: %v", err)
	}
	for _, s := range statements {
		if strings.TrimSpace(s.Text) == "" {
			t.Fatalf("%s is empty", s.ID())
		}
		if len(s.Tokens()) < 6 {
			t.Errorf("%s has %d tokens, fewer than the %d asked for: %s",
				s.ID(), len(s.Tokens()), 6, s.Text)
		}
		if len(s.Path) == 0 {
			t.Fatalf("%s names no productions", s.ID())
		}
		if s.Path[0] != grammar.DefaultStart {
			t.Errorf("%s starts at <%s>, want <%s>", s.ID(), s.Path[0], grammar.DefaultStart)
		}
		for _, name := range s.Path {
			if _, ok := g.Rule(name); !ok {
				t.Errorf("%s claims production <%s>, which the artifact does not define", s.ID(), name)
			}
		}
	}
}

// Coverage is printed in the report as the bound on what the whole phase could
// ever say, so it has to be a count of the artifact and not of the walk.
func TestCoverageIsBoundedByTheArtifact(t *testing.T) {
	cov := generator(t, 1, grammar.Options{}).Coverage()
	if cov.Total != 814 {
		t.Errorf("total %d, want the 814 productions of the artifact", cov.Total)
	}
	if cov.Reachable <= 0 || cov.Reachable > cov.Total {
		t.Errorf("%d reachable of %d", cov.Reachable, cov.Total)
	}
	if cov.Cut != len(reachableCuts(t)) {
		t.Errorf("%d productions replaced by a token, want %d", cov.Cut, len(reachableCuts(t)))
	}
	if cov.Start != grammar.DefaultStart {
		t.Errorf("coverage starts at %q", cov.Start)
	}
}

// reachableCuts is the subset of leaves.go a walk from the root actually meets.
// A cut for a production nothing reaches is not counted, which is why this is
// computed rather than written down.
func reachableCuts(t *testing.T) []string {
	t.Helper()
	g := load(t)
	seen, cuts := map[string]bool{}, []string{}
	var walk func(string)
	walk = func(name string) {
		name = strings.Trim(name, "<>")
		if seen[name] {
			return
		}
		seen[name] = true
		if _, isCut := grammar.Cut(name); isCut {
			cuts = append(cuts, name)
			return
		}
		r, ok := g.Rule(name)
		if !ok {
			return
		}
		for _, ref := range r.Body.Refs() {
			walk(ref)
		}
	}
	walk(grammar.DefaultStart)
	return cuts
}

// The reducer is the difference between a lead a person can read and forty
// tokens nobody will. What matters is that it gets smaller, that it keeps
// whatever made the statement worth reporting, and that every candidate it
// offered was still a statement of the language.
func TestReduceShrinksAStatementAndKeepsWhatWasAskedFor(t *testing.T) {
	gen := generator(t, 99, grammar.Options{MinTokens: 20})
	long, err := gen.Generate()
	if err != nil {
		t.Fatalf("walking: %v", err)
	}
	// The property under test stands in for a syntax error: whatever the
	// engine objected to, the reducer has to keep it.
	word := strings.Fields(long.Text)[0]
	asked := 0
	small := grammar.Reduce(long, func(candidate string) bool {
		asked++
		return strings.Contains(candidate, word)
	})
	if asked == 0 {
		t.Fatal("the reducer asked nothing")
	}
	if !strings.Contains(small.Text, word) {
		t.Errorf("the reduced statement lost %q: %s", word, small.Text)
	}
	if len(small.Tokens()) > len(long.Tokens()) {
		t.Errorf("reduction grew the statement from %d tokens to %d",
			len(long.Tokens()), len(small.Tokens()))
	}
	if len(small.Path) == 0 {
		t.Error("the reduced statement names no productions, so a reader cannot tell which one is in dispute")
	}
	if small.ID() != long.ID() {
		t.Errorf("reduction renamed the statement from %s to %s", long.ID(), small.ID())
	}
}

// A reducer that answers no to everything must hand back exactly what it was
// given. This is the path the runner takes when the engine stops answering
// mid-reduction, and a reducer that returned an empty statement there would
// have the report print a lead about nothing.
func TestReduceKeepsTheOriginalWhenNothingHolds(t *testing.T) {
	gen := generator(t, 3, grammar.Options{MinTokens: 12})
	s, err := gen.Generate()
	if err != nil {
		t.Fatalf("walking: %v", err)
	}
	got := grammar.Reduce(s, func(string) bool { return false })
	if got.Text != s.Text {
		t.Errorf("a reduction nothing held onto changed the statement:\n%s\n%s", s.Text, got.Text)
	}
}

func TestCasesDropRepeatsAndCiteNoClause(t *testing.T) {
	gen := generator(t, 11, grammar.Options{})
	statements, err := gen.GenerateN(60)
	if err != nil {
		t.Fatalf("walking: %v", err)
	}
	cases := grammar.Cases(statements)
	if len(cases) == 0 {
		t.Fatal("60 statements produced no cases")
	}
	if len(cases) > len(statements) {
		t.Fatalf("%d cases from %d statements", len(cases), len(statements))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.Query] {
			t.Errorf("%s repeats a statement an earlier case already sent", c.ID)
		}
		seen[c.Query] = true
		if c.Kind != corpus.KindGenerated {
			t.Errorf("%s is kind %q", c.ID, c.Kind)
		}
		// The one thing a generated case must never do is claim something from
		// the standard that nobody checked. Productions are the exception and
		// they are not a claim about behaviour: the walk used them.
		if len(c.Features) > 0 || len(c.Subclauses) > 0 || len(c.Conditions) > 0 {
			t.Errorf("%s cites the standard: features %v subclauses %v conditions %v",
				c.ID, c.Features, c.Subclauses, c.Conditions)
		}
		if c.Repeat != 1 {
			t.Errorf("%s asks for %d executions; a generated statement is not a measurement", c.ID, c.Repeat)
		}
	}
}

// The generated kind exists for the runner's benefit and must not become a way
// to smuggle unchecked statements into a corpus on disk.
func TestTheCorpusLoaderRefusesAGeneratedCase(t *testing.T) {
	const file = `
cases:
  - id: generated/smuggled
    name: A case that cites nothing
    kind: generated
    query: RETURN 1 AS v
    expect:
      kind: accept
`
	_, _, err := corpus.Load(fstest.MapFS{"c.yaml": &fstest.MapFile{Data: []byte(file)}}, nil)
	if err == nil {
		t.Fatal("a corpus file declaring the generated kind loaded")
	}
	if !strings.Contains(err.Error(), string(corpus.KindGenerated)) {
		t.Errorf("the refusal does not name the kind: %v", err)
	}
}

func TestAFingerprintFollowsTheStatementAndNotItsWhitespace(t *testing.T) {
	a := grammar.Fingerprint("RETURN 1 AS v")
	if b := grammar.Fingerprint("  RETURN 1 AS v\n"); a != b {
		t.Errorf("%q and %q fingerprint differently: %s and %s", "RETURN 1 AS v", "  RETURN 1 AS v\n", a, b)
	}
	if c := grammar.Fingerprint("RETURN 2 AS v"); a == c {
		t.Error("two different statements share a fingerprint")
	}
}

func TestThePromotionListRefusesAnEntryNobodyCanReExamine(t *testing.T) {
	stmt := "RETURN 1 AS v"
	print := grammar.Fingerprint(stmt)

	// A missing file is the normal state of a project that has never promoted
	// anything, and must not stop a run.
	empty, err := grammar.LoadPromoted(fstest.MapFS{}, "promoted.yaml")
	if err != nil {
		t.Fatalf("a missing promotion list is an error: %v", err)
	}
	if empty.Len() != 0 || empty.Has(stmt) {
		t.Error("an empty promotion list claims to know something")
	}

	ok, err := grammar.ParsePromoted([]byte(
		"promoted:\n  - fingerprint: "+print+"\n    statement: "+stmt+"\n    note: the engine documents this restriction\n"), "p.yaml")
	if err != nil {
		t.Fatalf("a complete entry was refused: %v", err)
	}
	if !ok.Has("  " + stmt) {
		t.Error("a promoted statement is not recognised through its fingerprint")
	}

	for name, doc := range map[string]string{
		"no reason at all":         "promoted:\n  - fingerprint: " + print + "\n    statement: " + stmt + "\n",
		"no fingerprint":           "promoted:\n  - statement: " + stmt + "\n    note: looked at it\n",
		"a fingerprint of nothing": "promoted:\n  - fingerprint: 0011223344556677\n    statement: " + stmt + "\n    note: looked at it\n",
		"a field nobody will read": "promoted:\n  - fingerprint: " + print + "\n    verdict: fine\n",
	} {
		if _, err := grammar.ParsePromoted([]byte(doc), "p.yaml"); err == nil {
			t.Errorf("the loader accepted an entry with %s", name)
		}
	}
}

// The shipped list is loaded on every Load, so a mistake in it stops every run
// including the ones that never ask for a walk.
func TestTheShippedPromotionListLoads(t *testing.T) {
	p, err := grammar.LoadEmbeddedPromoted()
	if err != nil {
		t.Fatalf("the shipped promotion list does not load: %v", err)
	}
	if p == nil {
		t.Fatal("no promotion list and no error")
	}
}
