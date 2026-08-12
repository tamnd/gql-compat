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
