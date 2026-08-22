package corpus_test

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/iso"
)

// The grammar register makes a stronger claim than the feature one, because the
// grammar is the denominator most of the work is left in and an entry here is a
// rule taken off the list of work. So it is attacked the same way: the shipped
// entries have to survive the checks the loader makes against ISO's artifacts,
// no case may cite one, and the loader has to refuse the entries somebody would
// write to make a number look better.

func TestShippedGrammarRegisterHoldsUp(t *testing.T) {
	known := codes(t)
	us, err := corpus.Uncitables(known)
	if err != nil {
		t.Fatalf("loading the register: %v", err)
	}
	if len(us) == 0 {
		t.Fatal("the register is empty; either it lost an entry or the loader lost the file")
	}
	registered := corpus.UncitableProductions(us)
	for _, u := range us {
		if _, ok := known.Catalog.Production(u.Production); !ok {
			t.Errorf("<%s> is not a rule in the grammar", u.Production)
			continue
		}
		switch u.Why {
		case corpus.Implementers:
			if !known.LeftToTheImplementation(u.Production) {
				t.Errorf("<%s> is not a rule the standard hands to the implementer", u.Production)
			}
		case corpus.BehindAnUnwritableFeature:
			if u.Feature == "" {
				t.Errorf("<%s> stands behind no feature", u.Production)
			}
		case corpus.Orphaned:
			from := known.Referrers(u.Production)
			if len(from) == 0 {
				t.Errorf("<%s> has no referrer at all, so it is a start symbol", u.Production)
			}
			for _, r := range from {
				if !registered[r] {
					t.Errorf("<%s> is reachable from <%s>, which is not registered", u.Production, r)
				}
			}
		default:
			t.Errorf("<%s> claims reason %q, which no check in this test covers", u.Production, u.Why)
		}
		t.Logf("<%s> %s", u.Production, u.Why)
	}
}

// A rule cannot be both cited and uncitable. If somebody writes the case, the
// register entry is the thing that is now wrong, and this is what says so.
func TestNoCaseCitesAnUncitableProduction(t *testing.T) {
	suite, cat := load(t)
	us, err := corpus.Uncitables(iso.Codes{Catalog: cat})
	if err != nil {
		t.Fatalf("loading the register: %v", err)
	}
	cannot := corpus.UncitableProductions(us)
	for _, c := range suite.Cases {
		for _, p := range c.Productions {
			if cannot[p] {
				t.Errorf("case %s cites <%s>, which the register says no case can cite; "+
					"delete the register entry or the citation", c.ID, p)
			}
		}
	}
}

// The register says nine rules are out of reach, which is a claim about the
// grammar and not about the corpus, so it has to hold when the corpus is not
// looking. Every rule the register does not name has to be reachable from a
// start symbol, and this walks the references to prove it.
//
// The grammar has more than one start symbol, which is the thing to know before
// reading this. <GQL-program> is where a program starts, and the lexical layer
// hangs off <token> and <GQL terminal character>, which nothing in the
// syntactic layer names: the standard hands the syntax a stream of tokens and
// spells the tokens separately. So the roots are every rule nothing names, and
// walking from <GQL-program> alone would call half of Clause 21 unreachable.
func TestEveryRuleOutsideTheRegisterIsReachable(t *testing.T) {
	known := codes(t)
	us, err := corpus.Uncitables(known)
	if err != nil {
		t.Fatalf("loading the register: %v", err)
	}
	registered := corpus.UncitableProductions(us)

	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] || registered[name] {
			return
		}
		seen[name] = true
		p, ok := known.Catalog.Production(name)
		if !ok {
			return
		}
		for _, r := range p.References {
			walk(r)
		}
	}
	roots := 0
	for _, p := range known.Productions {
		if len(known.Referrers(p.Name)) == 0 {
			roots++
			walk(p.Name)
		}
	}
	if !seen["GQL-program"] {
		t.Error("<GQL-program> is not a root of the grammar, which cannot be right")
	}
	t.Logf("%d rules reached from %d start symbols, %d registered as out of reach",
		len(seen), roots, len(registered))

	for _, p := range known.Productions {
		if seen[p.Name] || registered[p.Name] {
			continue
		}
		// A rule no walk reaches and the register does not name is one of two
		// things and both are worth a failing test: a rule that belongs in the
		// register, or a rule reachable only through one and therefore an
		// orphan somebody has not filed.
		t.Errorf("<%s> is reachable from no start symbol and is not registered, referrers %v",
			p.Name, known.Referrers(p.Name))
	}
}

func TestTheGrammarRegisterRefusesAnEntryItCannotCheck(t *testing.T) {
	known := codes(t)
	features, err := corpus.Unwritables(known)
	if err != nil {
		t.Fatalf("loading the feature register: %v", err)
	}
	entry := func(body string) string { return "version: 1\nuncitable:\n" + body }
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "a rule that does not exist",
			doc:  entry("  - production: wishful thinking\n    why: orphaned\n    note: made up\n"),
			want: "not a rule in the ISO/IEC 39075 grammar",
		},
		{
			name: "no production to check the claim against",
			doc:  entry("  - why: orphaned\n    note: trust me\n"),
			want: "no production",
		},
		{
			name: "no note",
			doc:  entry("  - production: procedure name\n    why: orphaned\n    note: \"  \"\n"),
			want: "no note",
		},
		{
			name: "the same rule twice",
			doc: entry("  - production: procedure name\n    why: orphaned\n    note: one\n" +
				"  - production: procedure name\n    why: orphaned\n    note: two\n"),
			want: "listed twice",
		},
		{
			name: "no reason",
			doc:  entry("  - production: procedure name\n    note: one\n"),
			want: "no reason",
		},
		{
			name: "a reason nobody defined",
			doc:  entry("  - production: procedure name\n    why: too hard\n    note: one\n"),
			want: "is not a reason this build knows",
		},
		{
			name: "an orphan the rest of the grammar can still reach",
			doc:  entry("  - production: match statement\n    why: orphaned\n    note: one\n"),
			want: "names it and is not registered",
		},
		{
			name: "an orphan nothing names, which is a start symbol",
			doc:  entry("  - production: GQL-program\n    why: orphaned\n    note: one\n"),
			want: "start symbol",
		},
		{
			name: "an implementers entry for a rule the grammar expands",
			doc:  entry("  - production: match statement\n    why: implementers\n    note: one\n"),
			want: "the grammar expands this rule",
		},
		{
			name: "an implementers entry for a rule no item names",
			doc:  entry("  - production: newline\n    why: implementers\n    note: one\n"),
			want: "the grammar expands this rule",
		},
		{
			name: "a feature entry for a feature the other register does not list",
			doc:  entry("  - production: external object reference\n    why: unwritable-feature\n    feature: GQ08\n    note: one\n"),
			want: "does not say GQ08 hangs off this rule",
		},
		{
			name: "a feature entry with no feature",
			doc:  entry("  - production: external object reference\n    why: unwritable-feature\n    note: one\n"),
			want: "no feature",
		},
		{
			name: "an orphan that names a feature it has no business naming",
			doc:  entry("  - production: procedure name\n    why: orphaned\n    feature: GP04\n    note: one\n"),
			want: "names no feature",
		},
		{
			name: "a schema this build does not read",
			doc:  "version: 2\nuncitable: []\n",
			want: "version 2",
		},
		{
			name: "a field nobody defined",
			doc:  entry("  - production: procedure name\n    why: orphaned\n    note: one\n    verdict: skip\n"),
			want: "uncitable:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := corpus.ReadUncitable([]byte(tc.doc), known, features)
			if err == nil {
				t.Fatal("the register loaded and should not have")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error is %q, which does not say %q", err, tc.want)
			}
		})
	}
}

// The distinction the register turns on, stated as a test because it is the one
// somebody will get wrong. A rule the grammar declines to expand is usually
// citable and usually cited, and it is only out of reach when ISO's own list of
// implementation-defined items names it as well.
func TestUnexpandedIsNotEnoughToBeLeftToTheImplementation(t *testing.T) {
	known := codes(t)
	for _, tc := range []struct {
		production string
		want       bool
	}{
		{"implementation-defined access mode", true},
		{"bidirectional control character", true},
		{"newline", false},
		{"whitespace", false},
		{"identifier start", false},
		{"string literal character", false},
		{"unsigned decimal in scientific notation", false},
		// Expanded in full, and named by the implementation-dependent list,
		// which is why being named is not the test on its own either.
		{"value expression", false},
		{"non-delimited identifier", false},
		{"match statement", false},
	} {
		if got := known.LeftToTheImplementation(tc.production); got != tc.want {
			t.Errorf("<%s>: left to the implementation is %v, want %v", tc.production, got, tc.want)
		}
	}
}
