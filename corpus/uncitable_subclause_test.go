package corpus_test

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/iso"
)

// The subclause register is the one with the most room to become a rubber
// stamp, because a subclause number is cheap to write down and 317 of them is a
// lot of work. So it is attacked hardest: the shipped entries have to survive
// the checks the loader makes, no case may cite one, and the loader has to
// refuse every entry somebody would reach for to make the open column smaller.

func TestShippedSubclauseRegisterHoldsUp(t *testing.T) {
	known := codes(t)
	us, err := corpus.UncitableSubclauses(known)
	if err != nil {
		t.Fatalf("loading the register: %v", err)
	}
	if len(us) == 0 {
		t.Fatal("the register is empty; either it lost an entry or the loader lost the file")
	}
	rules, err := corpus.Uncitables(known)
	if err != nil {
		t.Fatalf("loading the grammar register: %v", err)
	}
	registered := corpus.UncitableProductions(rules)
	for _, u := range us {
		if !known.Subclause(u.Subclause) {
			t.Errorf("%s is not a normative subclause", u.Subclause)
			continue
		}
		title, _ := known.SubclauseTitle(u.Subclause)
		switch u.Why {
		case corpus.SpellsARegisteredRule:
			// The claim is that the subclause is its rules, so every rule ISO's
			// own title names has to be one the grammar register holds. A title
			// naming none of them is a subclause about something else.
			named := 0
			for rest := title; ; {
				open := strings.Index(rest, "<")
				if open < 0 {
					break
				}
				shut := strings.Index(rest[open:], ">")
				if shut < 0 {
					break
				}
				name := rest[open+1 : open+shut]
				rest = rest[open+shut+1:]
				named++
				if !registered[name] {
					t.Errorf("%s is titled %q and <%s> is not in the grammar register", u.Subclause, title, name)
				}
			}
			if named == 0 {
				t.Errorf("%s is titled %q, which names no grammar rule", u.Subclause, title)
			}
		case corpus.ImplementationInternal:
			// The claim is that the grammar has no name for the thing, so the
			// entry has to say what the thing is, ISO's own title has to agree,
			// and no rule may be named after it. Anything the title does name has
			// to be a rule a case can write, because that is where the case goes.
			switch {
			case u.Object == "":
				t.Errorf("%s claims its subject is unnameable and does not say what the subject is", u.Subclause)
			case !strings.HasPrefix(u.Subclause, "4."):
				t.Errorf("%s is outside Clause 4 and claims a reason only Clause 4 has", u.Subclause)
			case !strings.Contains(strings.ToLower(title), strings.ToLower(u.Object)):
				t.Errorf("%s says it is about %q and ISO titles it %q", u.Subclause, u.Object, title)
			}
			if named := known.ProductionsNaming(u.Object); len(named) > 0 {
				t.Errorf("%s says nothing names %q and the grammar has <%s>", u.Subclause, u.Object, named[0])
			}
			for rest := title; ; {
				open := strings.Index(rest, "<")
				if open < 0 {
					break
				}
				shut := strings.Index(rest[open:], ">")
				if shut < 0 {
					break
				}
				name := rest[open+1 : open+shut]
				rest = rest[open+shut+1:]
				if !known.Production(name) {
					t.Errorf("%s is titled %q and <%s> is not a rule in the grammar", u.Subclause, title, name)
				}
				if registered[name] {
					t.Errorf("%s is titled %q and <%s> is in the grammar register, so the entry belongs under %q",
						u.Subclause, title, name, corpus.SpellsARegisteredRule)
				}
			}
		default:
			t.Errorf("%s claims reason %q, which no check in this test covers", u.Subclause, u.Why)
		}
		t.Logf("%-8s %s  %s", u.Subclause, title, u.Why)
	}
}

// A subclause cannot be both cited and uncitable. If somebody writes the case,
// the register entry is the thing that is now wrong, and this is what says so.
func TestNoCaseCitesAnUncitableSubclause(t *testing.T) {
	suite, cat := load(t)
	us, err := corpus.UncitableSubclauses(iso.Codes{Catalog: cat})
	if err != nil {
		t.Fatalf("loading the register: %v", err)
	}
	cannot := corpus.UncitableSubclauseNumbers(us)
	for _, c := range suite.Cases {
		for _, s := range c.Subclauses {
			if cannot[s] {
				t.Errorf("case %s cites subclause %s, which the register says no case can cite; "+
					"delete the register entry or the citation", c.ID, s)
			}
		}
	}
}

func TestTheSubclauseRegisterRefusesAnEntryItCannotCheck(t *testing.T) {
	known := codes(t)
	rules, err := corpus.Uncitables(known)
	if err != nil {
		t.Fatalf("loading the grammar register: %v", err)
	}
	entry := func(body string) string { return "version: 1\nuncitable:\n" + body }
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "a subclause that does not exist",
			doc:  entry("  - subclause: \"99.9\"\n    why: registered-rule\n    note: made up\n"),
			want: "not a normative subclause",
		},
		{
			name: "front matter, which specifies nothing to conform to",
			doc:  entry("  - subclause: \"3.1\"\n    why: registered-rule\n    note: terms\n"),
			want: "not a normative subclause",
		},
		{
			name: "no subclause to check the claim against",
			doc:  entry("  - why: registered-rule\n    note: trust me\n"),
			want: "no subclause",
		},
		{
			name: "no note",
			doc:  entry("  - subclause: \"15.3\"\n    why: registered-rule\n    note: \"  \"\n"),
			want: "no note",
		},
		{
			name: "the same subclause twice",
			doc: entry("  - subclause: \"15.3\"\n    why: registered-rule\n    note: one\n" +
				"  - subclause: \"15.3\"\n    why: registered-rule\n    note: two\n"),
			want: "listed twice",
		},
		{
			name: "no reason",
			doc:  entry("  - subclause: \"15.3\"\n    note: one\n"),
			want: "no reason",
		},
		{
			name: "a reason nobody defined",
			doc:  entry("  - subclause: \"15.3\"\n    why: too hard\n    note: one\n"),
			want: "is not a reason this build knows",
		},
		{
			// The one that matters. Clause 4 is where the work is and its
			// subclauses are titled in words, so there is nothing for an entry
			// to stand behind and the loader says so.
			name: "a subclause ISO titles in words rather than in rules",
			doc:  entry("  - subclause: \"4.2.2\"\n    why: registered-rule\n    note: nobody can cite an agent\n"),
			want: "names no grammar rule",
		},
		{
			name: "a subclause whose rule the grammar register does not hold",
			doc:  entry("  - subclause: \"14.4\"\n    why: registered-rule\n    note: one\n"),
			want: "the grammar register does not hold",
		},
		{
			// The second reason's own way of becoming a rubber stamp is an entry
			// that names a thing the grammar does have a rule for, so this is the
			// one that keeps it honest. Clause 4 is full of words a statement says.
			name: "a subject the grammar names",
			doc:  entry("  - subclause: \"4.4\"\n    why: implementation-internal\n    object: value\n    note: values are everywhere\n"),
			want: "so a statement can say",
		},
		{
			name: "a subject ISO's own title does not mention",
			doc:  entry("  - subclause: \"4.1\"\n    why: implementation-internal\n    object: execution context\n    note: one\n"),
			want: "which does not say that",
		},
		{
			name: "no subject to look for in the grammar",
			doc:  entry("  - subclause: \"4.1\"\n    why: implementation-internal\n    note: one\n"),
			want: "no object",
		},
		{
			name: "a subclause outside the clause that defines the standard's words",
			doc:  entry("  - subclause: \"15.3\"\n    why: implementation-internal\n    object: named procedure call\n    note: one\n"),
			want: "this reason is for Clause 4",
		},
		{
			name: "a subject on an entry whose reason reads its subjects out of the title",
			doc:  entry("  - subclause: \"15.3\"\n    why: registered-rule\n    object: procedure\n    note: one\n"),
			want: "reads its objects out of the rules",
		},
		{
			name: "a schema this build does not read",
			doc:  "version: 2\nuncitable: []\n",
			want: "version 2",
		},
		{
			name: "a field nobody defined",
			doc:  entry("  - subclause: \"15.3\"\n    why: registered-rule\n    note: one\n    verdict: skip\n"),
			want: "uncitable subclause:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := corpus.ReadUncitableSubclauses([]byte(tc.doc), known, rules)
			if err == nil {
				t.Fatal("the register loaded and should not have")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error is %q, which does not say %q", err, tc.want)
			}
		})
	}
}
