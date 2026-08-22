package iso

import "testing"

// TestLoadCounts pins the shape of the artifacts as fetched. A change here
// means ISO republished something, and every conformance percentage in every
// report moved with it; that should be a deliberate commit, not a surprise.
func TestLoadCounts(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tc := range []struct {
		what string
		got  int
		want int
	}{
		{"features", len(c.Features), 228},
		{"condition classes", len(c.Classes), 12},
		{"implementation-defined", len(c.ImplementationDefined), 117},
		{"productions", len(c.Productions), 814},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.what, tc.got, tc.want)
		}
	}
	subclasses := 0
	for _, cl := range c.Classes {
		subclasses += len(cl.Subclasses)
	}
	if subclasses != 68 {
		t.Errorf("subclasses = %d, want 68", subclasses)
	}
	if len(c.ImplementationDependent) == 0 {
		t.Error("implementation-dependent is empty")
	}
	seeTheRules := 0
	for _, p := range c.Productions {
		if p.SeeTheRules {
			seeTheRules++
		}
	}
	if seeTheRules != 23 {
		t.Errorf("rules the grammar declines to expand = %d, want 23", seeTheRules)
	}
}

// TestSeeTheRules checks the marker that tells a generator which rules it
// cannot reach. A rule ISO writes as "!! See the Syntax Rules." has no
// right-hand side to expand, so anything walking the grammar for inputs stops
// there, and a feature reachable only through one of those rules has no syntax
// the standard owns.
func TestSeeTheRules(t *testing.T) {
	c := MustLoad()
	for _, name := range []string{"external object reference", "identifier start", "SQL-datetime literal"} {
		p, ok := c.Production(name)
		if !ok {
			t.Fatalf("<%s> missing", name)
		}
		if !p.SeeTheRules {
			t.Errorf("<%s> should be a rule the grammar leaves to the rules", name)
		}
		if len(p.References) != 0 || len(p.Keywords) != 0 {
			t.Errorf("<%s> expands into something after all: %v %v", name, p.References, p.Keywords)
		}
	}
	// A rule with a right-hand side must not pick up the marker, including one
	// whose alternatives mention a rule that has it.
	for _, name := range []string{"match statement", "copy of graph type"} {
		p, ok := c.Production(name)
		if !ok {
			t.Fatalf("<%s> missing", name)
		}
		if p.SeeTheRules {
			t.Errorf("<%s> is expanded by the grammar and must not carry the marker", name)
		}
	}
	codes := Codes{Catalog: c}
	if !codes.SeeTheRules("external object reference") {
		t.Error("Codes.SeeTheRules says <external object reference> expands")
	}
	if codes.SeeTheRules("no such rule") {
		t.Error("Codes.SeeTheRules said yes to a rule that does not exist")
	}
}

func TestLookups(t *testing.T) {
	c := MustLoad()
	f, ok := c.Feature("GQ08")
	if !ok || f.Description != "FILTER statement" {
		t.Errorf("Feature(GQ08) = %q, %v", f.Description, ok)
	}
	if f.Family != "GQ" {
		t.Errorf("GQ08 family = %q, want GQ", f.Family)
	}
	if g, ok := c.Feature("G002"); !ok || g.Family != "G" {
		t.Errorf("Feature(G002) family = %q, %v", g.Family, ok)
	}
	if _, ok := c.Feature("ZZ99"); ok {
		t.Error("Feature(ZZ99) should not exist")
	}
	if _, ok := c.Production("match statement"); !ok {
		t.Error("production <match statement> missing")
	}
	if _, ok := c.Status("22G03"); !ok {
		t.Error("GQLSTATUS 22G03 should be defined")
	}
	if _, ok := c.Status("99Z99"); ok {
		t.Error("GQLSTATUS 99Z99 should not be defined")
	}
}

// TestFamiliesCoverEveryFeature guards against a feature code whose prefix
// the families table does not name, which would silently drop it from the
// per-family rollup in every report.
func TestFamiliesCoverEveryFeature(t *testing.T) {
	c := MustLoad()
	known := map[string]bool{}
	for _, f := range families {
		known[f.Prefix] = true
	}
	for _, f := range c.Features {
		if !known[f.Family] {
			t.Errorf("feature %s has unnamed family %q", f.Code, f.Family)
		}
	}
	total := 0
	for _, f := range c.Families() {
		total += f.Count
	}
	if total != len(c.Features) {
		t.Errorf("family counts sum to %d, want %d", total, len(c.Features))
	}
}

func TestGrammarExtraction(t *testing.T) {
	c := MustLoad()
	p, ok := c.Production("GQL-program")
	if !ok {
		t.Fatal("<GQL-program> missing")
	}
	if len(p.References) == 0 {
		t.Error("<GQL-program> references nothing")
	}
	kws := c.Keywords()
	if len(kws) < 200 {
		t.Errorf("only %d keywords extracted, expected the grammar to spell out far more", len(kws))
	}
	want := map[string]bool{"MATCH": false, "RETURN": false, "SESSION": false, "TRAIL": false}
	for _, k := range kws {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("keyword %s not extracted from the grammar", k)
		}
	}
}

// TestAKeywordIsRequiredOnlyWhenEveryAlternativeSpellsIt guards the difference
// between a rule that mentions a word and a rule that makes you say it.
//
// The distinction is the whole value of the field. <return statement> begins
// RETURN and there is no other way in, so a case citing it and never spelling
// RETURN cited the wrong rule. <is or colon> mentions IS and accepts a colon
// instead; <element variable declaration> mentions TEMP and puts it in
// brackets; <aggregate function> mentions COUNT in one alternative of four.
// Read those three as required and the check built on this field would reject
// correct citations, which is the failure that gets a check deleted.
func TestAKeywordIsRequiredOnlyWhenEveryAlternativeSpellsIt(t *testing.T) {
	c := MustLoad()
	for _, tc := range []struct {
		name string
		want bool
		why  string
	}{
		{"return statement", true, "RETURN is the only way in"},
		{"insert statement", true, "INSERT is the only way in"},
		{"session set schema clause", true, "SCHEMA is not optional"},
		{"is or colon", false, "a colon will do instead of IS"},
		{"element variable declaration", false, "TEMP is in brackets"},
		{"aggregate function", false, "COUNT is one alternative of several"},
		{"record constructor", false, "RECORD is in brackets"},
		{"primitive result statement", false, "FINISH is one alternative of two"},
		{"node pattern", false, "the rule spells no keyword at all"},
	} {
		p, ok := c.Production(tc.name)
		if !ok {
			t.Errorf("<%s> missing from the grammar", tc.name)
			continue
		}
		if p.RequiresKeyword != tc.want {
			t.Errorf("<%s> requires a keyword = %v, want %v: %s", tc.name, p.RequiresKeyword, tc.want, tc.why)
		}
	}
	// A sanity bound on the whole grammar. If this drops to nothing the walk
	// has stopped seeing <alt> boundaries, and if it climbs to most of the
	// grammar it has stopped seeing <opt>.
	n := 0
	for _, p := range c.Productions {
		if p.RequiresKeyword {
			n++
		}
	}
	if n < 50 || n > len(c.Productions)/3 {
		t.Errorf("%d of %d productions require a keyword, which is not a believable share", n, len(c.Productions))
	}
}

// Referrers is the grammar read backwards, and reading it backwards is what
// lets a caller say a rule is out of reach. The table is one rule of each shape
// that matters: a start symbol, a rule with exactly one way in, and a rule half
// the grammar names.
func TestReferrersReadsTheGrammarBackwards(t *testing.T) {
	c := MustLoad()
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"GQL-program", nil},
		{"procedure name", []string{"catalog procedure parent and name"}},
		{"procedure argument list", []string{"named procedure call"}},
		// Two ways in, which is the answer worth having: <optional operand> is
		// how OPTIONAL MATCH is spelled, and a reader who assumed one referrer
		// would have missed half the rule's uses.
		{"simple match statement", []string{"match statement", "optional operand"}},
	} {
		got := c.Referrers(tc.name)
		if len(got) != len(tc.want) {
			t.Errorf("<%s> is named by %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("<%s> is named by %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
	// Angle brackets are stripped the way Production strips them, so a caller
	// holding a name in either spelling gets the same answer.
	if len(c.Referrers("<procedure name>")) != 1 {
		t.Error("a name in angle brackets did not resolve")
	}
	// Every rule but a handful is named by something. If this climbs the walk
	// has stopped following references.
	roots := 0
	for _, p := range c.Productions {
		if len(c.Referrers(p.Name)) == 0 {
			roots++
		}
	}
	if roots < 1 || roots > 30 {
		t.Errorf("%d of %d rules are named by nothing, which is not a believable share of start symbols",
			roots, len(c.Productions))
	}
}
