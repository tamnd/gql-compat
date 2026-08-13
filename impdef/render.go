package impdef

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Result is the observation phase's whole output.
//
// The two totals travel with the observations because the honest form of "we
// observed eleven choices" is "eleven of the hundred and seventeen the standard
// delegates", and a reader who only has the list cannot supply the
// denominator. It is the same rule the coverage tables follow.
type Result struct {
	// DefinedTotal and DependentTotal are ISO's own counts, from the two
	// vendored artifacts.
	DefinedTotal   int `json:"implementation_defined_total"`
	DependentTotal int `json:"implementation_dependent_total"`
	// Observations is one entry per probe, in probe order.
	Observations []Observation `json:"observations"`
}

// Len reports how many probes ran.
func (r *Result) Len() int {
	if r == nil {
		return 0
	}
	return len(r.Observations)
}

// Of returns the observations of one kind, in order.
func (r *Result) Of(k Kind) []Observation {
	if r == nil {
		return nil
	}
	var out []Observation
	for _, o := range r.Observations {
		if o.Kind == k {
			out = append(out, o)
		}
	}
	return out
}

// Items counts the distinct ISO items the probes of one kind cite, which is
// the numerator the section prints against ISO's own total. Two probes of one
// item are two observations but one item answered.
func (r *Result) Items(k Kind) int {
	seen := map[string]bool{}
	for _, o := range r.Of(k) {
		seen[o.Item] = true
	}
	return len(seen)
}

// Observed counts the observations of one kind that reached an answer.
func (r *Result) Observed(k Kind) int {
	n := 0
	for _, o := range r.Of(k) {
		if o.Observed() {
			n++
		}
	}
	return n
}

// Heading is the section title, shared by every renderer so the markdown, the
// HTML and the table of contents cannot drift apart.
const Heading = "Choices the standard leaves open"

// Preamble is the paragraph that has to be above the tables.
//
// It says three things and each of them has been got wrong somewhere before: an
// observation is not a score, the section is in no total, and a dash is not a
// zero. Rule R4 of the reporting spec asks for the last of those on every table
// that can print one.
const Preamble = "ISO/IEC 39075 does not specify everything. It delegates some behaviour to the " +
	"implementation, which Clause 24.5.2 then obliges to write down what it chose, and some to the " +
	"running system, which nobody has to document and no program may depend on. Clause 24.5.3 permits " +
	"extensions on the same terms as the first: say what they are. What follows is what this run could " +
	"observe of all three. None of it is scored. It is excluded from the scoreboard, from every coverage " +
	"denominator and from the exit status, because an item the standard declined to decide has no right " +
	"answer for an engine to miss. An em dash means the probe reached no answer, which is not the same " +
	"as none and not the same as unlimited."

// KindHeading is the title each of the three tables gets. The wording is about
// who chose, because that is the distinction the whole section rests on. It is
// exported for the same reason Heading is: the HTML report renders its own
// tables and must not invent its own words for them.
func KindHeading(k Kind) string {
	switch k {
	case Defined:
		return "What the implementation chose"
	case Dependent:
		return "What the running system chose"
	default:
		return "Extensions"
	}
}

// KindPreamble is the paragraph above each table, which carries the ISO
// denominator for that kind and the reason the kind exists at all.
func KindPreamble(k Kind, r *Result) string {
	switch k {
	case Defined:
		return fmt.Sprintf("These are the items Clause 24.5.2 asks an implementation to state. This harness probes %d of the %d, "+
			"and %d of those probes reached an answer on this engine. The other %d are not in the table below at all: "+
			"`gql-compat statement` prints the whole list as a template, with this run's answers filled in and a dash beside "+
			"everything a vendor still has to answer themselves.",
			r.Items(Defined), r.DefinedTotal, r.Observed(Defined), r.DefinedTotal-r.Items(Defined))
	case Dependent:
		return fmt.Sprintf("These are the %d items the standard leaves to the running system, of which %d are probed here. "+
			"Nobody is obliged to document any of them and a portable program may not depend on any of them, "+
			"which is exactly why they are worth printing: an engine's answer here is a fact about this run "+
			"and may be a different fact tomorrow.",
			r.DependentTotal, r.Items(Dependent))
	default:
		return "Syntax this engine accepts that the standard does not define. Clause 24.5.3 permits that, on the condition " +
			"that the implementation documents it, and IE005 makes the treatment of non-conforming language " +
			"implementation-defined in the first place. Undefined is not forbidden: an engine accepting one of these " +
			"has extended the language, and the note under each says what the grammar artifact does and does not contain."
	}
}

// WriteSection renders the section as markdown.
//
// It is one function rather than a method on the report because the consensus
// command, the statement template and the report all want the same words, and
// the one rule this section has to keep is easier to keep in one place: no
// entry may carry an outcome. There is no code path here that can print one.
func WriteSection(w io.Writer, r *Result) error {
	b := bufio.NewWriter(w)
	defer func() { _ = b.Flush() }()
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }

	p("## %s\n\n", Heading)
	p("%s\n\n", Preamble)
	if r.Len() == 0 {
		p("No probe ran. The observation phase is off, or the run's selector excluded it.\n\n")
		return b.Flush()
	}

	for _, k := range AllKinds {
		obs := r.Of(k)
		if len(obs) == 0 {
			continue
		}
		p("### %s\n\n", KindHeading(k))
		p("%s\n\n", KindPreamble(k, r))
		if k == Extension {
			p("| Extension | Observed | The statement |\n|---|---|---|\n")
			for _, o := range obs {
				p("| %s | %s | %s |\n", cell(o.Question), cell(o.Cell()), Code(o.Statement))
			}
		} else {
			p("| Item | What ISO leaves open | What was asked | Observed | The statement |\n|---|---|---|---|---|\n")
			for _, o := range obs {
				p("| `%s` | %s | %s | %s | %s |\n",
					o.Item, cell(o.Description), cell(o.Question), cell(o.Cell()),
					Code(o.Statement))
			}
		}
		p("\n")
		notes := 0
		for _, o := range obs {
			if o.Note == "" {
				continue
			}
			if notes == 0 {
				p("Notes:\n\n")
			}
			notes++
			p("- **`%s`** %s\n", o.Item, oneLine(o.Note))
		}
		if notes > 0 {
			p("\n")
		}
	}
	return b.Flush()
}

// Cell is the observation as a reader should see it: the value, or an em dash
// and the reason there is none.
func (o Observation) Cell() string {
	if o.Observed() {
		return Escape(o.Display())
	}
	return "— (" + string(o.Silence) + ")"
}

// Escape renders the characters that would damage the document they are
// printed into.
//
// A right-to-left override in a table cell reverses everything after it, a
// zero-width joiner is invisible, and a probe about whether an engine accepts
// either of those has to print what it sent. The engine received the character;
// the report shows its code point. Everything else, including every ordinary
// non-ASCII letter, is printed as itself, because a report that escaped é would
// be answering IV001 with a lie about what was asked.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsGraphic(r) && !unicode.Is(unicode.Bidi_Control, r) && !unicode.Is(unicode.Join_Control, r):
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, `\u%04X`, r)
		}
	}
	return b.String()
}

// cell makes a value safe for a markdown table cell.
func cell(s string) string {
	s = oneLine(s)
	return strings.ReplaceAll(s, "|", `\|`)
}

// Code renders a statement as a markdown code span.
//
// It exists because the two things a code span cannot survive are both in this
// package's probes. One asks whether an engine pads character strings, so it
// contains two consecutive spaces and a renderer that collapsed them would
// print a different question from the one that was asked; the other delimits an
// identifier with accent quotes, which are the character a code span is fenced
// with. So the whitespace is left alone and the fence grows until it is longer
// than any run of backticks inside it, which is what CommonMark asks for.
func Code(s string) string {
	s = strings.ReplaceAll(Escape(s), "|", `\|`)
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}
