package consensus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/runner"
)

// WriteJSON writes the whole comparison.
func WriteJSON(w io.Writer, r *Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// WriteMarkdown renders the comparison for a human.
//
// The order is the argument. What was compared, then the disclaimer, then the
// queue, then the agreement table, then the divided cases. The disclaimer is
// above the queue rather than below it because a reader who stops after the
// first screen must not leave with a list of engine findings, and the last
// thing on the page is the limit of the method rather than a conclusion.
func WriteMarkdown(w io.Writer, r *Result) error {
	b := bufio.NewWriter(w)
	defer func() { _ = b.Flush() }()
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	nl := func() { fmt.Fprintln(b) }

	p("# Corpus review queue: cases every engine failed\n")
	nl()

	p("## What was compared\n")
	nl()
	p("| Engine | Version | Mode | Cases | Report |\n|---|---|---|---|---|\n")
	for _, e := range r.Engines {
		p("| `%s` | %s | %s | %d | `%s` |\n",
			e.Adapter, or(e.Version, "unrecorded"), or(string(e.Mode), "unrecorded"), e.Cases, e.Source)
	}
	nl()
	if r.Thin {
		p("**Two engines only.** %s\n", thinWarning(r.EngineNames()))
		nl()
	}
	if r.MixedModes() {
		p("**These reports answer different questions.** The runs are in %s mode, "+
			"and a case judged under one mode's rules is not the same case under another's. "+
			"Read the queue below as a list of things to look at, not as agreement.\n",
			strings.Join(r.Modes(), " and "))
		nl()
	}

	p("## How to read this\n")
	nl()
	p("%s\n", Disclaimer)
	nl()
	p("%s\n", Limit)
	nl()

	writeQueue(b, r)

	a := r.Summarize()
	p("## Agreement\n")
	nl()
	p("| | Cases |\n|---|---|\n")
	p("| Every judging engine passed | %d |\n", a.AllPassed)
	p("| Every judging engine failed | %d |\n", a.AllFailed)
	p("| Engines disagreed | %d |\n", a.Divided)
	p("| Fewer than two engines judged it | %d |\n", a.Unjudged)
	p("| Total cases seen in any report | %d |\n", a.Total)
	nl()
	p("An engine that skipped a case or errored on it did not judge it, so it is counted in neither direction. "+
		"The %d cases fewer than two engines judged are not evidence of agreement or of disagreement; "+
		"they are cases this comparison cannot speak about.\n", a.Unjudged)
	nl()

	writeDivided(b, r)
	return nil
}

// thinWarning is the sentence a two-engine comparison gets. It names the
// engines because whether two engines are independent is a fact about which
// two they are, and a reader who knows the lineage can weigh it.
func thinWarning(names []string) string {
	return fmt.Sprintf("Two engines failing the same case is a coin flip, not a consensus. "+
		"%s might share a parser generation, a Cypher heritage, or the same reading of a sentence in the standard, "+
		"and nothing here can tell the difference. Add a third engine before acting on anything below.",
		strings.Join(names, " and "))
}

func writeQueue(b *bufio.Writer, r *Result) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	nl := func() { fmt.Fprintln(b) }

	p("## The queue\n")
	nl()
	if len(r.Review) == 0 {
		p("No case was failed by every engine that judged it. That is the expected state and it is not a result: " +
			"it means this comparison found nothing to review, not that the corpus is correct.\n")
		nl()
		return
	}

	undisposed := len(r.Undisposed())
	p("%d case%s failed by every engine that judged %s. ",
		len(r.Review), plural(len(r.Review)), pronoun(len(r.Review)))
	if undisposed == 0 {
		p("Every one of them has a written disposition.\n")
	} else {
		p("%d of them have no disposition written yet.\n", undisposed)
	}
	nl()

	kinds := r.KindCounts()
	if len(kinds) > 0 {
		parts := make([]string, 0, len(kinds))
		for _, k := range sortedKinds(kinds) {
			parts = append(parts, fmt.Sprintf("%d %s", kinds[k], k))
		}
		p("By kind: %s. A queue that is all one kind is usually one mistake made once.\n", strings.Join(parts, ", "))
		nl()
	}

	if by := r.ByVerdict(); len(by) > 0 {
		p("| Disposition | Cases |\n|---|---|\n")
		for _, v := range verdicts {
			if n := len(by[v]); n > 0 {
				p("| `%s` | %d |\n", v, n)
			}
		}
		p("| undisposed | %d |\n", undisposed)
		nl()
	}

	for _, c := range r.Review {
		p("### `%s`\n", c.ID)
		nl()
		if c.Name != "" {
			p("%s\n", c.Name)
			nl()
		}
		if c.Kind != "" {
			p("Kind `%s`. ", c.Kind)
		} else {
			p("")
		}
		p("Failed by %s.", strings.Join(c.Failed, ", "))
		if len(c.Skipped) > 0 {
			p(" Skipped by %s.", strings.Join(c.Skipped, ", "))
		}
		if len(c.Errored) > 0 {
			p(" Errored on %s.", strings.Join(c.Errored, ", "))
		}
		nl()
		nl()
		p("| Engine | Outcome | Reason |\n|---|---|---|\n")
		for _, v := range c.Verdicts {
			p("| `%s` | %s | %s |\n", v.Engine, v.Outcome, cell(v.Reason))
		}
		nl()
		if d, ok := r.Dispositions[c.ID]; ok {
			p("**Disposition `%s`.** %s", d.Verdict, d.Note)
			if d.Rule != "" {
				p(" Load rule %s now rejects a case written this way.", d.Rule)
			}
			if d.Decided != "" {
				p(" Decided %s.", d.Decided)
			}
			nl()
		} else {
			p("**No disposition.** Nobody has read this one yet.\n")
		}
		nl()
	}

	if undisposed > 0 {
		p("### Starting a review\n")
		nl()
		p("The undisposed cases above need a human to read the case and the standard and write down which it is. " +
			"A skeleton for the file, with the verdicts left blank so it will not load until they are filled in:\n")
		nl()
		p("```yaml\n%s```\n", r.Template())
		nl()
		p("A disposition of `%s` is the only one that obliges anybody to do anything: fix the case, and where the mistake "+
			"is one the corpus loader could have caught, add a load rule so the next case like it cannot be written.\n", CorpusBug)
		nl()
	}
}

func writeDivided(b *bufio.Writer, r *Result) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	nl := func() { fmt.Fprintln(b) }

	var divided []*Case
	for _, c := range r.Cases {
		if c.Divided() {
			divided = append(divided, c)
		}
	}
	p("## Where the engines disagreed\n")
	nl()
	if len(divided) == 0 {
		p("Nowhere, which for a corpus meant to separate engines is a finding about the corpus.\n")
		nl()
		return
	}
	p("%d case%s some engines passed and others failed. These are the cases doing their job and none of them is under review here; "+
		"the list is for orientation, and the engine reports are where the detail is.\n", len(divided), plural(len(divided)))
	nl()
	p("| Case | Passed | Failed |\n|---|---|---|\n")
	for _, c := range divided {
		p("| `%s` | %s | %s |\n", c.ID, strings.Join(c.Passed, ", "), strings.Join(c.Failed, ", "))
	}
	nl()
}

// WriteText prints the short form, for a terminal.
func WriteText(w io.Writer, r *Result) error {
	b := bufio.NewWriter(w)
	defer func() { _ = b.Flush() }()
	a := r.Summarize()

	fmt.Fprintf(b, "%s\n", strings.Join(r.EngineNames(), " vs "))
	fmt.Fprintf(b, "  %d cases, %d agreed pass, %d agreed fail, %d divided, %d judged by fewer than two\n",
		a.Total, a.AllPassed, a.AllFailed, a.Divided, a.Unjudged)
	if r.Thin {
		fmt.Fprintf(b, "  two engines only: agreement here is a coin flip, add a third before acting on it\n")
	}
	if len(r.Review) == 0 {
		fmt.Fprintf(b, "  queue empty\n")
		return nil
	}
	fmt.Fprintf(b, "\n%d case(s) every judging engine failed. This is a corpus review queue, not an engine finding.\n\n",
		len(r.Review))
	for _, c := range r.Review {
		d, ok := r.Dispositions[c.ID]
		mark := "?"
		if ok {
			mark = d.Verdict
		}
		fmt.Fprintf(b, "  %-14s %-52s failed by %s\n", mark, c.ID, strings.Join(c.Failed, ", "))
	}
	if n := len(r.Undisposed()); n > 0 {
		fmt.Fprintf(b, "\n%d of them have no disposition. Nothing here changes any pass rate.\n", n)
	}
	if stale := r.Stale(); len(stale) > 0 {
		fmt.Fprintf(b, "\n%d disposition(s) name cases the queue no longer holds: %s\n",
			len(stale), strings.Join(stale, ", "))
	}
	return nil
}

// KindCounts groups the queue by case kind, which is the first thing a
// reviewer wants: a queue that is all one kind is usually one mistake.
func (r *Result) KindCounts() map[corpus.Kind]int {
	out := map[corpus.Kind]int{}
	for _, c := range r.Review {
		out[c.Kind]++
	}
	return out
}

// OutcomeCounts is how many of each outcome one engine recorded across the
// compared cases, for a caller building its own summary.
func (r *Result) OutcomeCounts(engine string) map[runner.Outcome]int {
	out := map[runner.Outcome]int{}
	for _, c := range r.Cases {
		for _, v := range c.Verdicts {
			if v.Engine == engine {
				out[v.Outcome]++
			}
		}
	}
	return out
}

// cell makes a reason safe for a markdown table cell: pipes break the table,
// and a newline in a reason ends the row early.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	if s == "" {
		return "&mdash;"
	}
	return s
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// sortedKinds is the kinds present in the queue, in a stable order.
func sortedKinds(m map[corpus.Kind]int) []corpus.Kind {
	out := make([]corpus.Kind, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
