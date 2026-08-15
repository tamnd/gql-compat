package corpus_test

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/iso"
)

// The register says a feature has no portable case and never will, which is a
// claim strong enough to be worth attacking. What follows attacks it from both
// sides: the shipped entries have to survive the checks the loader can make
// against ISO's own artifacts, and the loader has to refuse an entry that
// would let a merely untested feature be filed as an impossible one.

func codes(t *testing.T) iso.Codes {
	t.Helper()
	cat, err := iso.Load()
	if err != nil {
		t.Fatalf("loading the ISO catalogue: %v", err)
	}
	return iso.Codes{Catalog: cat}
}

func TestShippedRegisterHoldsUp(t *testing.T) {
	known := codes(t)
	us, err := corpus.Unwritables(known)
	if err != nil {
		t.Fatalf("loading the register: %v", err)
	}
	if len(us) == 0 {
		t.Fatal("the register is empty; either it lost an entry or the loader lost the file")
	}
	for _, u := range us {
		f, ok := known.Catalog.Feature(u.Feature)
		if !ok {
			t.Errorf("%s is not one of the 228", u.Feature)
			continue
		}
		p, ok := known.Catalog.Production(u.Production)
		if !ok {
			t.Errorf("%s cites <%s>, which is not a rule in the grammar", u.Feature, u.Production)
			continue
		}
		if !p.SeeTheRules {
			t.Errorf("%s cites <%s>, which the grammar does expand, so a case for it is writable",
				u.Feature, u.Production)
		}
		t.Logf("%s %s, at <%s>", u.Feature, f.Description, u.Production)
	}
}

// A feature cannot be both tested and untestable. If somebody writes the case,
// the register entry is the thing that is now wrong, and this is what says so.
func TestNoCaseClaimsAnUnwritableFeature(t *testing.T) {
	suite, cat := load(t)
	us, err := corpus.Unwritables(iso.Codes{Catalog: cat})
	if err != nil {
		t.Fatalf("loading the register: %v", err)
	}
	cannot := corpus.UnwritableCodes(us)
	for _, c := range suite.Cases {
		for _, f := range c.Features {
			if cannot[f] {
				t.Errorf("case %s claims %s, which the register says no portable case can claim; "+
					"delete the register entry or the claim", c.ID, f)
			}
		}
	}
}

func TestTheRegisterRefusesAnEntryItCannotCheck(t *testing.T) {
	known := codes(t)
	entry := func(body string) string { return "version: 1\nunwritable:\n" + body }
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "a feature ISO never defined",
			doc:  entry("  - feature: GZ99\n    production: external object reference\n    note: made up\n"),
			want: "no such optional feature",
		},
		{
			name: "a rule the grammar does expand",
			doc:  entry("  - feature: GH01\n    production: match statement\n    note: not really\n"),
			want: "not a rule the grammar leaves to the implementer",
		},
		{
			name: "a rule that does not exist",
			doc:  entry("  - feature: GH01\n    production: wishful thinking\n    note: not really\n"),
			want: "not a rule the grammar leaves to the implementer",
		},
		{
			name: "no production to check the claim against",
			doc:  entry("  - feature: GH01\n    note: trust me\n"),
			want: "no production",
		},
		{
			name: "no reason",
			doc:  entry("  - feature: GH01\n    production: external object reference\n    note: \"  \"\n"),
			want: "no note",
		},
		{
			name: "the same feature twice",
			doc: entry("  - feature: GH01\n    production: external object reference\n    note: one\n" +
				"  - feature: GH01\n    production: external object reference\n    note: two\n"),
			want: "listed twice",
		},
		{
			name: "a schema this build does not read",
			doc:  "version: 2\nunwritable: []\n",
			want: "version 2",
		},
		{
			name: "a field nobody defined",
			doc:  entry("  - feature: GH01\n    production: external object reference\n    note: one\n    verdict: skip\n"),
			want: "unwritable:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := corpus.ReadUnwritable([]byte(tc.doc), known)
			if err == nil {
				t.Fatal("the register loaded and should not have")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error is %q, which does not say %q", err, tc.want)
			}
		})
	}
}
