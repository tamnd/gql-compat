package impdef_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/iso"
)

// The rule this package exists to keep is that nothing here is a verdict. An
// implementation-defined choice cannot be wrong, so the section must not be
// readable as a scoreboard, must not put a number on anything, and must say
// "not observed" where a scoreboard would say zero. Most of what follows is
// that rule.

// verdicts are the four words a case result can carry. They are written out
// rather than imported from the runner because the runner imports this package
// and the point is that this package never learns them.
var verdicts = []string{"pass", "fail", "skip", "error"}

func catalogue(t *testing.T) iso.Codes {
	t.Helper()
	cat, err := iso.Load()
	if err != nil {
		t.Fatalf("loading the ISO catalogue: %v", err)
	}
	return iso.Codes{Catalog: cat}
}

func shipped(t *testing.T) *impdef.Set {
	t.Helper()
	set, err := impdef.LoadEmbedded(catalogue(t))
	if err != nil {
		t.Fatalf("loading the shipped probes: %v", err)
	}
	if set.Len() == 0 {
		t.Fatal("the shipped probe document is empty")
	}
	return set
}

// answered is every shipped probe as if the engine had answered it, with a
// value chosen to be the most awkward thing a table cell can hold.
func answered(t *testing.T, value string) *impdef.Result {
	t.Helper()
	set := shipped(t)
	r := &impdef.Result{DefinedTotal: 117, DependentTotal: 20}
	for _, p := range set.Probes {
		r.Observations = append(r.Observations, p.Observe(nil, &fakeFailure{msg: value}))
	}
	return r
}

// fakeFailure is an engine refusal carrying whatever text the test wants,
// including text a report must never repeat.
type fakeFailure struct{ msg string }

func (f *fakeFailure) Error() string { return f.msg }

// TestTheSectionCarriesNoVerdictVocabulary is the exit criterion for the whole
// package. The engine's own words go into the archive and no further: a parser
// that answers a probe with "syntax error near ';'" has said something true
// about its own grammar and nothing at all about whether the choice ISO
// delegated was made correctly, and a reader who found the word in a table of
// choices would reasonably conclude that some of them are wrong.
func TestTheSectionCarriesNoVerdictVocabulary(t *testing.T) {
	hostile := "SYNTAX ERROR: the statement failed to parse; case skipped, 0 passed"
	var b bytes.Buffer
	if err := impdef.WriteSection(&b, answered(t, hostile)); err != nil {
		t.Fatal(err)
	}
	got := strings.ToLower(b.String())
	for _, v := range verdicts {
		if strings.Contains(got, v) {
			t.Errorf("the rendered section contains %q; an implementation-defined choice has no outcome", v)
		}
	}
	if !strings.Contains(got, strings.ToLower(impdef.Heading)) {
		t.Error("the section rendered without its own heading")
	}
}

// TestTheEngineOwnWordsSurviveInTheArchive is the other half of the rule above.
// Suppressing the engine's message from the prose is only defensible if it is
// still somewhere: a maintainer debugging why eleven probes went silent needs
// the parser's actual complaint.
func TestTheEngineOwnWordsSurviveInTheArchive(t *testing.T) {
	hostile := "SYNTAX ERROR at offset 3"
	r := answered(t, hostile)
	found := 0
	for _, o := range r.Observations {
		if o.Detail == hostile {
			found++
		}
	}
	if found != len(r.Observations) {
		t.Errorf("%d of %d observations kept the engine's words; the JSON archive is where they belong",
			found, len(r.Observations))
	}
}

// TestAnUnobservedChoiceIsADashAndNotAZero holds the same line the metrics
// tables hold. An engine that was never asked what its maximum identifier
// length is has not said it is unlimited and has not said it is zero.
func TestAnUnobservedChoiceIsADashAndNotAZero(t *testing.T) {
	set := shipped(t)
	p := set.Probes[0]
	o := p.Silent(impdef.NoSession, "connection refused")

	if o.Observed() {
		t.Fatal("a silent observation reports itself as observed")
	}
	if o.Display() != "—" {
		t.Errorf("an unobserved value displays as %q, want an em dash", o.Display())
	}
	if !strings.HasPrefix(o.Cell(), "—") {
		t.Errorf("the cell is %q, want it to begin with an em dash", o.Cell())
	}
	if !strings.Contains(o.Cell(), string(impdef.NoSession)) {
		t.Errorf("the cell is %q and does not say why nothing was observed", o.Cell())
	}

	var b bytes.Buffer
	r := &impdef.Result{DefinedTotal: 117, DependentTotal: 20, Observations: []impdef.Observation{o}}
	if err := impdef.WriteSection(&b, r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "—") {
		t.Error("the rendered section has no em dash for the probe that observed nothing")
	}
	// The prose above the table is allowed to say the words "none" and
	// "unlimited", because saying a dash is neither of them is the point of it.
	// The row is not.
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		for _, wrong := range []string{"| 0 |", "unlimited", "| none |"} {
			if strings.Contains(line, wrong) {
				t.Errorf("a row renders an unobserved choice as %q: %s", wrong, line)
			}
		}
	}
}

// TestAnEmptyStringIsObservedAndSaysSo separates the two things an empty cell
// could mean. TRIM returning nothing is an answer; a probe that never ran is
// not, and both would render as an empty table cell if nobody said otherwise.
func TestAnEmptyStringIsObservedAndSaysSo(t *testing.T) {
	o := impdef.Observation{ID: "x", Item: "IA010", Kind: impdef.Defined}
	if !o.Observed() {
		t.Fatal("an observation with no silence is not observed")
	}
	if o.Display() != "the empty string" {
		t.Errorf("an observed empty value displays as %q", o.Display())
	}
}

// TestInvisibleCharactersArePrintedAsCodePoints. Two probes send characters
// that would damage the document they are reported in: a right-to-left
// override reverses everything after it in a table cell, and a private-use
// code point renders as whatever the reader's font decides. The engine got the
// character; the report shows its number.
func TestInvisibleCharactersArePrintedAsCodePoints(t *testing.T) {
	got := impdef.Escape("a\u202Eb\uE000c\td")
	want := `a\u202Eb\uE000c\td`
	if got != want {
		t.Errorf("Escape gave %q, want %q", got, want)
	}
	// An ordinary letter is not a hazard, and escaping it would misreport what
	// the non-ASCII source probe actually asked.
	if got := impdef.Escape("héllo ☃"); got != "héllo ☃" {
		t.Errorf("Escape mangled ordinary text: %q", got)
	}
}

// TestAStatementIsPrintedExactlyAsItWasSent. Two of the probes would be
// misreported by an ordinary code span: one asks whether an engine pads
// character strings, so it contains two consecutive spaces, and one delimits an
// identifier with the accent quotes a code span is fenced with. A reader who
// copied either row out of the report has to get the statement that ran.
func TestAStatementIsPrintedExactlyAsItWasSent(t *testing.T) {
	if got := impdef.Code("RETURN 'a' = 'a  ' AS v"); !strings.Contains(got, "'a  '") {
		t.Errorf("the two spaces were collapsed: %s", got)
	}
	got := impdef.Code("RETURN 1 AS `x`")
	if !strings.HasPrefix(got, "``") || !strings.HasSuffix(got, "``") {
		t.Errorf("a statement containing a backtick was fenced with one: %s", got)
	}
	if !strings.Contains(got, "`x`") {
		t.Errorf("the delimited identifier did not survive: %s", got)
	}

	var b bytes.Buffer
	if err := impdef.WriteSection(&b, answered(t, "no")); err != nil {
		t.Fatal(err)
	}
	// A row with an odd number of backticks in it has an unterminated code
	// span, which swallows the rest of the table.
	for line := range strings.SplitSeq(b.String(), "\n") {
		if strings.HasPrefix(line, "| `") && strings.Count(line, "`")%2 != 0 {
			t.Errorf("unbalanced code fences in a row: %s", line)
		}
	}
}

// TestNoShippedProbeWrites. The observation phase runs against the same graphs
// the cases run against and restores nothing afterwards, so a probe that
// deleted a node would change the answer of whatever case ran next.
func TestNoShippedProbeWrites(t *testing.T) {
	for _, p := range shipped(t).Probes {
		if err := p.Validate(catalogue(t)); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
	}
	bad := &impdef.Probe{
		ID: "x", Item: "IA010", Kind: impdef.Defined, Question: "q", Read: impdef.Cell,
		Statement: "MATCH (n) DETACH DELETE n",
	}
	if err := bad.Validate(catalogue(t)); err == nil {
		t.Error("a probe that deletes every node was accepted")
	}
	// The same keyword inside a literal is not a write, and a probe about
	// string handling may well contain one.
	ok := &impdef.Probe{
		ID: "y", Item: "IA015", Kind: impdef.Defined, Question: "q", Read: impdef.Cell,
		Statement: "RETURN 'DELETE' = 'delete' AS v",
	}
	if err := ok.Validate(catalogue(t)); err != nil {
		t.Errorf("a literal containing a keyword was read as a write: %v", err)
	}
}

// TestEveryProbeCitesAnItemOnTheRightList. The item number is what makes an
// observation a statement about the standard rather than an anecdote about a
// query, so a probe that cites the wrong list, or an item on neither, must not
// load at all.
func TestEveryProbeCitesAnItemOnTheRightList(t *testing.T) {
	known := catalogue(t)
	for _, p := range shipped(t).Probes {
		if _, ok := known.Item(p.Item); !ok {
			t.Errorf("%s cites %s, which is on neither list", p.ID, p.Item)
		}
		if p.Kind == impdef.Dependent && known.Defined(p.Item) {
			t.Errorf("%s calls %s implementation-dependent, and it is not", p.ID, p.Item)
		}
		if p.Kind != impdef.Dependent && !known.Defined(p.Item) {
			t.Errorf("%s cites %s as implementation-defined, and it is not", p.ID, p.Item)
		}
	}
	for _, bad := range []string{"IA999", "", "ZZ001"} {
		p := &impdef.Probe{ID: "x", Item: bad, Kind: impdef.Defined, Question: "q",
			Statement: "RETURN 1", Read: impdef.Cell}
		if err := p.Validate(known); err == nil {
			t.Errorf("a probe citing %q loaded", bad)
		}
	}
}

// TestAnExtensionMustBeDocumented. Clause 24.5.3 permits an extension on one
// condition, which is that the implementation says what it is. A probe with no
// note would be reporting undocumented syntax as though the clause allowed it.
func TestAnExtensionMustBeDocumented(t *testing.T) {
	known := catalogue(t)
	p := &impdef.Probe{ID: "x", Item: "IE005", Kind: impdef.Extension, Question: "q",
		Statement: "RETURN 1 AS v;", Read: impdef.Accepted}
	if err := p.Validate(known); err == nil {
		t.Error("an extension probe with no note was accepted")
	}
	p.Note = "the semicolon appears in none of the 814 productions"
	if err := p.Validate(known); err != nil {
		t.Errorf("a documented extension was rejected: %v", err)
	}
	for _, e := range shipped(t).Probes {
		if e.Kind == impdef.Extension && strings.TrimSpace(e.Note) == "" {
			t.Errorf("shipped extension %s has no note", e.ID)
		}
	}
}

// TestARefusalIsAnAnswerToSomeQuestionsAndNotToOthers. Asked what happens on
// integer overflow, an engine that rejects the statement has answered. Asked
// whether 'a' and 'a  ' compare equal, an engine that rejects the statement has
// not chosen a padding rule and the honest observation is silence.
func TestARefusalIsAnAnswerToSomeQuestionsAndNotToOthers(t *testing.T) {
	refusal := &fakeFailure{msg: "no"}
	answer := (&impdef.Probe{ID: "a", Read: impdef.Answer}).Observe(nil, refusal)
	if !answer.Observed() || answer.Value != "refused" {
		t.Errorf("a refusal to an answer probe gave %+v, want the refusal as the value", answer)
	}
	cell := (&impdef.Probe{ID: "c", Read: impdef.Cell}).Observe(nil, refusal)
	if cell.Observed() {
		t.Error("a refusal was read as a choice for a probe a refusal does not answer")
	}
	if cell.Silence != impdef.Refused {
		t.Errorf("silence is %q, want %q", cell.Silence, impdef.Refused)
	}
}

// TestTheStatementTemplateCarriesEveryItemISONames, not only the ones probed.
// A vendor filling in a 24.5.2 statement needs the hundred-odd items this
// harness cannot ask about more than they need the eleven it can.
func TestTheStatementTemplateCarriesEveryItemISONames(t *testing.T) {
	cat, err := iso.Load()
	if err != nil {
		t.Fatal(err)
	}
	var items []impdef.Item
	for _, b := range cat.ImplementationDefined {
		items = append(items, impdef.Item{Code: b.Code, Description: b.Description, Kind: impdef.Defined})
	}
	for _, b := range cat.ImplementationDependent {
		items = append(items, impdef.Item{Code: b.Code, Description: b.Description, Kind: impdef.Dependent})
	}

	var b bytes.Buffer
	r := &impdef.Result{DefinedTotal: len(cat.ImplementationDefined), DependentTotal: len(cat.ImplementationDependent)}
	if err := impdef.WriteStatement(&b, impdef.Statement{Engine: "zu"}, items, r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, it := range items {
		if !strings.Contains(out, "`"+it.Code+"`") {
			t.Fatalf("the template omits %s, which an implementer still has to state", it.Code)
		}
	}
	if n := strings.Count(out, "| — | — |"); n < len(cat.ImplementationDefined) {
		t.Errorf("%d rows are dashed with nothing observed, want at least %d",
			n, len(cat.ImplementationDefined))
	}
	if !strings.Contains(out, "24.5.2") || !strings.Contains(out, "24.5.3") {
		t.Error("the template does not cite the clauses that require it")
	}
}

// TestTheSectionStatesTheISODenominator. Eleven observations is not a
// measurement of anything until the reader knows it is eleven of a hundred and
// seventeen, which is the same rule the coverage tables follow.
func TestTheSectionStatesTheISODenominator(t *testing.T) {
	var b bytes.Buffer
	r := answered(t, "no")
	if err := impdef.WriteSection(&b, r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "117") || !strings.Contains(out, "20") {
		t.Error("the section does not state ISO's own totals for the two lists")
	}
	if r.Items(impdef.Defined) >= r.DefinedTotal {
		t.Errorf("this harness claims to probe %d of %d implementation-defined items",
			r.Items(impdef.Defined), r.DefinedTotal)
	}
}
