package corpus_test

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/iso"
)

// The embedded suite is the product this package ships. Every assertion below
// is about the suite itself rather than about the loader: the loader is
// exercised by loading it, and what needs guarding is that a case cannot be
// added which claims something ISO does not define, or which no engine could
// be judged against fairly.

func load(t *testing.T) (*corpus.Suite, *iso.Catalog) {
	t.Helper()
	cat, err := iso.Load()
	if err != nil {
		t.Fatalf("loading the ISO catalogue: %v", err)
	}
	suite, fixtures, err := corpus.LoadEmbedded(iso.Codes{Catalog: cat})
	if err != nil {
		t.Fatalf("loading the embedded suite: %v", err)
	}
	if suite.Len() == 0 {
		t.Fatal("the embedded suite is empty")
	}
	if fixtures == nil {
		t.Fatal("the embedded suite has no fixtures")
	}
	return suite, cat
}

func TestEmbeddedSuiteLoads(t *testing.T) {
	suite, _ := load(t)
	byKind := map[corpus.Kind]int{}
	for _, c := range suite.Cases {
		byKind[c.Kind]++
	}
	// Every kind must be represented. A suite that had quietly lost its
	// condition cases would still load, still pass, and still report a
	// conformance percentage — over a corpus that no longer tested errors.
	for _, k := range corpus.AllKinds {
		if byKind[k] == 0 {
			t.Errorf("the suite contains no %s cases", k)
		}
	}
	t.Logf("%d cases: %v", suite.Len(), byKind)
}

func TestCaseIDsAreUnique(t *testing.T) {
	suite, _ := load(t)
	seen := map[string]string{}
	for _, c := range suite.Cases {
		if first, dup := seen[c.ID]; dup {
			t.Errorf("case id %q appears in both %s and %s", c.ID, first, c.Source)
			continue
		}
		seen[c.ID] = c.Source
	}
}

func TestCaseIDsMatchTheirKind(t *testing.T) {
	suite, _ := load(t)
	prefix := map[corpus.Kind]string{
		corpus.KindMandatory:   "mandatory/",
		corpus.KindOptional:    "optional/",
		corpus.KindCondition:   "condition/",
		corpus.KindGrammar:     "grammar/",
		corpus.KindPerformance: "performance/",
	}
	for _, c := range suite.Cases {
		if want := prefix[c.Kind]; !strings.HasPrefix(c.ID, want) {
			t.Errorf("%s: a %s case should have an id beginning %q", c.ID, c.Kind, want)
		}
	}
}

func TestMandatoryCasesClaimNoOptionalFeature(t *testing.T) {
	suite, _ := load(t)
	// This is the rule the whole mandatory/optional split rests on. If a
	// mandatory case used an optional feature, an engine could fail it by
	// lawfully declining that feature, and the suite would be reporting a
	// conformance defect where the standard permits a choice.
	for _, c := range suite.Cases {
		if c.Kind != corpus.KindMandatory {
			continue
		}
		if len(c.Features) > 0 {
			t.Errorf("%s: a mandatory case claims optional feature(s) %v", c.ID, c.Features)
		}
		if len(c.Requires) > 0 {
			t.Errorf("%s: a mandatory case requires optional feature(s) %v", c.ID, c.Requires)
		}
	}
}

func TestOptionalCasesNameKnownFeatures(t *testing.T) {
	suite, cat := load(t)
	for _, c := range suite.Cases {
		for _, f := range append(append([]string{}, c.Features...), c.Requires...) {
			if _, ok := cat.Feature(f); !ok {
				t.Errorf("%s: %q is not a feature in features.xml", c.ID, f)
			}
		}
	}
}

func TestRowExpectationsAreWellFormed(t *testing.T) {
	suite, _ := load(t)
	for _, c := range suite.Cases {
		if c.Expect.Kind != corpus.ExpectRows {
			continue
		}
		for i, row := range c.Expect.Rows {
			if len(row) != len(c.Expect.Columns) {
				t.Errorf("%s: row %d has %d values for %d columns",
					c.ID, i, len(row), len(c.Expect.Columns))
			}
		}
		// An ordered expectation of more than one row is a claim that the
		// engine must produce them in that order. Unless the statement sorts,
		// that claim is not in the standard, and the case would be scoring a
		// coin toss.
		if !c.Expect.Unordered && len(c.Expect.Rows) > 1 && !mentionsOrderBy(c.Query) {
			t.Errorf("%s: expects %d rows in order but the statement has no ORDER BY;"+
				" either sort it or mark the expectation unordered",
				c.ID, len(c.Expect.Rows))
		}
	}
}

func mentionsOrderBy(q string) bool {
	return strings.Contains(strings.ToUpper(q), "ORDER BY")
}

func TestErrorExpectationsNameAStatus(t *testing.T) {
	suite, cat := load(t)
	for _, c := range suite.Cases {
		if c.Expect.Kind != corpus.ExpectError {
			continue
		}
		if c.Expect.GQLStatus == "" {
			// Permitted by the model, but a condition case that does not say
			// which condition it expects is measuring only that something
			// went wrong, which every engine can achieve by accident.
			if c.Kind == corpus.KindCondition {
				t.Errorf("%s: a condition case must name the GQLSTATUS it expects", c.ID)
			}
			continue
		}
		if _, ok := cat.Status(c.Expect.GQLStatus); !ok {
			t.Errorf("%s: GQLSTATUS %q is not in conditions.xml", c.ID, c.Expect.GQLStatus)
		}
	}
}

// A diagnostic assertion is checked field by field against ISO's own
// vocabulary, because a case that asked for a subject kind of "node" would
// fail every engine over a word the standard does not use.
func TestDiagnosticAssertionsAreWellFormed(t *testing.T) {
	suite, _ := load(t)
	seen := 0
	for _, c := range suite.Cases {
		d := c.Expect.Diagnostic
		if d == nil {
			continue
		}
		seen++
		if c.Expect.Kind != corpus.ExpectError {
			t.Errorf("%s: a diagnostic record belongs to a condition, not to a %s case", c.ID, c.Expect.Kind)
		}
		if c.Expect.GQLStatus == "" {
			t.Errorf("%s: a case that asserts a record must name the status the record hangs off", c.ID)
		}
		if (d.Subject == "") != (d.SubjectKind == "") {
			t.Errorf("%s: a subject and its kind go together or not at all", c.ID)
		}
		if d.SubjectKind != "" && !slices.Contains(corpus.SubjectKinds, d.SubjectKind) {
			t.Errorf("%s: %q is not a subject kind ISO 39075 subclause 23.2 names", c.ID, d.SubjectKind)
		}
		if d.Subject != "" && !strings.Contains(c.Query, d.Subject) {
			t.Errorf("%s: the record is asserted to be about %q, which the query never writes", c.ID, d.Subject)
		}
		if !slices.Contains(c.Features, "GA08") {
			t.Errorf("%s: asserting a record is testing GA08, so the case has to claim it", c.ID)
		}
	}
	if seen == 0 {
		t.Error("no case asserts a diagnostic record, so GA08 is claimed and never measured")
	}
}

func TestMutatingCasesAreMarked(t *testing.T) {
	suite, _ := load(t)
	// A write that is not declared leaves the next case reading a graph that
	// is not the fixture it named, and the failure surfaces somewhere else
	// entirely. The check is textual because it has to catch the case the
	// author forgot to think about, and it matches whole words: OFFSET ends
	// in SET.
	writes := regexp.MustCompile(`(?i)\b(INSERT|SET|REMOVE|DELETE|CREATE|DROP|SESSION)\b`)
	for _, c := range suite.Cases {
		if c.Mutating || c.Expect.Kind == corpus.ExpectReject {
			continue
		}
		for _, s := range append([]string{c.Query}, c.Setup...) {
			if m := writes.FindString(s); m != "" {
				t.Errorf("%s: statement contains %q but the case is not marked mutating", c.ID, m)
				break
			}
		}
	}
}

func TestPerformanceCasesStillAssertAnAnswer(t *testing.T) {
	suite, _ := load(t)
	for _, c := range suite.Cases {
		if c.Kind != corpus.KindPerformance {
			continue
		}
		switch c.Expect.Kind {
		case corpus.ExpectRows, corpus.ExpectAccept:
		default:
			t.Errorf("%s: a performance case expects %q; it should assert rows,"+
				" or accept where the answer is not predictable", c.ID, c.Expect.Kind)
		}
		if c.Fixture == "" {
			t.Errorf("%s: a performance case with no fixture measures the parser", c.ID)
		}
	}
}

func TestEveryFeatureFamilyIsCovered(t *testing.T) {
	suite, cat := load(t)
	claimed := map[string]bool{}
	perFamily := map[string]int{}
	for _, c := range suite.Cases {
		for _, f := range c.Features {
			if claimed[f] {
				continue
			}
			claimed[f] = true
			if feat, ok := cat.Feature(f); ok {
				perFamily[feat.Family]++
			}
		}
	}
	// Not every one of the 228 features is testable through a portable
	// statement, so this does not demand full coverage. It demands that no
	// family is missed entirely, which is the failure mode of writing a
	// corpus one clause at a time and stopping.
	for _, fam := range cat.Families() {
		if fam.Count > 0 && perFamily[fam.Prefix] == 0 {
			t.Errorf("no case claims any of the %d features of family %s (%s)",
				fam.Count, fam.Prefix, fam.Name)
		}
	}
	t.Logf("%d of %d optional features claimed by at least one case", len(claimed), len(cat.Features))
}

// A case names things: the columns it declares it will get back, and the
// labels, edge types and property names its fixture puts in the graph. Every
// one of those positions is an <identifier> in ISO 21.3, and a reserved word
// spelled plainly is not one. A case that picks a reserved word is asking
// every conforming engine to refuse the statement, and the refusal is then
// published against the engine rather than against the case.
//
// This is a regression test for exactly that: `start`, `finish`, `same`,
// `size` and the label `Value` were all reserved words the corpus was using
// as names, and they cost the first engine strict enough to say so eight
// failures it had not earned.
//
// The rule is checked on the names the corpus declares rather than on the
// statement text, because telling an identifier from a keyword inside a
// statement needs the parser this harness deliberately does not have. A
// declared column and a fixture label are unambiguous, and in practice a case
// that avoids a reserved word in those two places has avoided it everywhere.
func TestNoCaseNamesSomethingWithAReservedWord(t *testing.T) {
	cat, err := iso.Load()
	if err != nil {
		t.Fatalf("loading the ISO catalogue: %v", err)
	}
	suite, fixtures, err := corpus.LoadEmbedded(iso.Codes{Catalog: cat})
	if err != nil {
		t.Fatalf("loading the embedded suite: %v", err)
	}

	for _, c := range suite.Cases {
		for _, col := range c.Expect.Columns {
			if cat.Reserved(col) {
				t.Errorf("%s declares the column %q, which ISO 21.3 reserves; rename it", c.ID, col)
			}
		}
	}
	for _, name := range fixtures.Names() {
		f, _ := fixtures.Get(name)
		for _, n := range f.Nodes {
			for _, l := range n.Labels {
				if cat.Reserved(l) {
					t.Errorf("fixture %s gives node %s the label %q, which ISO 21.3 reserves", name, n.Key, l)
				}
			}
			for p := range n.Props {
				if cat.Reserved(p) {
					t.Errorf("fixture %s gives node %s the property %q, which ISO 21.3 reserves", name, n.Key, p)
				}
			}
		}
		for _, e := range f.Edges {
			if cat.Reserved(e.Type) {
				t.Errorf("fixture %s gives an edge the type %q, which ISO 21.3 reserves", name, e.Type)
			}
			for p := range e.Props {
				if cat.Reserved(p) {
					t.Errorf("fixture %s gives an edge the property %q, which ISO 21.3 reserves", name, p)
				}
			}
		}
	}
}

// TestACaseCannotReadAPropertyItsFixtureNeverGenerates is a regression test
// for a corpus bug that cost an engine three failures it had not earned:
// three performance cases queried n.p0 and n.p1 against a fixture declared
// with no properties at all, and the engine's entirely correct "unknown
// property" was published against it. Neither the engine nor a reader of the
// report can tell that apart from a real defect, so the corpus must refuse to
// load in that state.
func TestACaseCannotReadAPropertyItsFixtureNeverGenerates(t *testing.T) {
	cat, err := iso.Load()
	if err != nil {
		t.Fatalf("loading the ISO catalogue: %v", err)
	}
	known := iso.Codes{Catalog: cat}

	const doc = `
fixtures:
  - name: two-props
    description: A chain with p0 and p1.
    generated: {shape: path, nodes: 10, properties: %d, seed: 1}
cases:
  - id: performance/filter/reads-p1
    name: Reads the second selectivity property
    kind: performance
    fixture: two-props
    subclauses: ["14.6"]
    query: |
      MATCH (n:N)
      WHERE n.p1 = 3 AND n.name <> '.p9'
      RETURN COUNT(*) AS n
    expect:
      kind: rows
      columns: [n]
      rows: [[1]]
`
	// Two properties are p0 and p1, so the case is legitimate.
	if _, _, err := corpus.Load(oneFile(fmt.Sprintf(doc, 2)), known); err != nil {
		t.Fatalf("a case reading p1 from a fixture with two properties must load: %v", err)
	}
	// One property is p0 only, so p1 will never exist.
	_, _, err = corpus.Load(oneFile(fmt.Sprintf(doc, 1)), known)
	if err == nil {
		t.Fatal("a case reading p1 from a fixture with one property must not load")
	}
	for _, want := range []string{"reads-p1", "p1", "two-props"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q so the fix is obvious; got: %v", want, err)
		}
	}
	// The `.p9` inside a string literal must not be read as a property.
	if strings.Contains(err.Error(), "p9") {
		t.Errorf("text inside a string literal was mistaken for a property read: %v", err)
	}
}

func oneFile(body string) fs.FS {
	return fstest.MapFS{"suite.yaml": &fstest.MapFile{Data: []byte(body)}}
}
