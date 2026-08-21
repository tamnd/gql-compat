// Package report renders a run into the formats different readers need.
//
// The same report goes out five ways. JSON is the archive and the only
// complete one — everything the runner measured is in it, and the other four
// are views. Markdown is what a person reads. HTML is that with a table of
// contents and colour. CSV is what goes into a spreadsheet or a plotting
// script. JUnit is what a CI system understands.
//
// One rule runs through all of them: a metric that was not available is
// rendered as unavailable, never as zero. Page-fault counters are Linux-only,
// a server engine's data directory is not on this machine, and a sampler that
// never got a reading has nothing to report. Printing a zero for any of those
// would put a number in a comparison that no measurement produced.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/runner"
)

// Format is an output rendering.
type Format string

// The formats this package can write. One run produces all five; JSON is the
// only lossless one, and the other four are views of it.
const (
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
	FormatHTML     Format = "html"
	FormatCSV      Format = "csv"
	FormatJUnit    Format = "junit"
)

// AllFormats lists every format, in the order the CLI documents them.
var AllFormats = []Format{FormatJSON, FormatMarkdown, FormatHTML, FormatCSV, FormatJUnit}

// Extension is the file suffix a format conventionally uses.
func (f Format) Extension() string {
	switch f {
	case FormatMarkdown:
		return ".md"
	case FormatHTML:
		return ".html"
	case FormatCSV:
		return ".csv"
	case FormatJUnit:
		return ".xml"
	default:
		return ".json"
	}
}

// ParseFormat resolves a name, accepting the common aliases.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json":
		return FormatJSON, nil
	case "md", "markdown":
		return FormatMarkdown, nil
	case "html":
		return FormatHTML, nil
	case "csv":
		return FormatCSV, nil
	case "junit", "xml":
		return FormatJUnit, nil
	}
	return "", fmt.Errorf("unknown report format %q (have: %s)", s, joinFormats())
}

func joinFormats() string {
	parts := make([]string, len(AllFormats))
	for i, f := range AllFormats {
		parts[i] = string(f)
	}
	return strings.Join(parts, ", ")
}

// Write renders one report in one format.
func Write(w io.Writer, rep *runner.Report, f Format) error {
	switch f {
	case FormatJSON:
		return WriteJSON(w, rep)
	case FormatMarkdown:
		return WriteMarkdown(w, rep)
	case FormatHTML:
		return WriteHTML(w, rep)
	case FormatCSV:
		return WriteCSV(w, rep)
	case FormatJUnit:
		return WriteJUnit(w, rep)
	}
	return fmt.Errorf("unknown report format %q", f)
}

// failures returns the cases a reader will want to look at first.
func failures(rep *runner.Report) []runner.CaseResult {
	var out []runner.CaseResult
	for _, c := range rep.Cases {
		if c.Outcome == runner.Fail || c.Outcome == runner.Error {
			out = append(out, c)
		}
	}
	return out
}

// skipsByReason groups skips so the report can say, in one line each, what an
// engine's data model kept it from being asked.
func skipsByReason(rep *runner.Report) []skipGroup {
	byReason := map[runner.SkipReason][]string{}
	for _, c := range rep.Cases {
		if c.Outcome != runner.Skip {
			continue
		}
		byReason[c.Skip] = append(byReason[c.Skip], c.ID)
	}
	out := make([]skipGroup, 0, len(byReason))
	for reason, ids := range byReason {
		sort.Strings(ids)
		out = append(out, skipGroup{Reason: reason, IDs: ids})
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].IDs) > len(out[j].IDs) })
	return out
}

type skipGroup struct {
	Reason runner.SkipReason
	IDs    []string
}

// kindRows returns the scoreboard in the fixed order AllKinds defines, so two
// reports line up column for column even when one has no cases of a kind.
func kindRows(rep *runner.Report) []kindRow {
	out := make([]kindRow, 0, len(corpus.AllKinds))
	for _, k := range corpus.AllKinds {
		t, ok := rep.Totals.ByKind[k]
		if !ok {
			continue
		}
		out = append(out, kindRow{Kind: k, KindTotals: t})
	}
	return out
}

type kindRow struct {
	Kind corpus.Kind
	runner.KindTotals
}

// unwritableGroup is the entries of a coverage register that share a reason.
//
// A report says why a feature has no case, and the why is the same sentence
// for every entry that shares a reason, so it is written once above the list
// rather than once per entry. Grouping also keeps the count honest when a
// second reason arrives: a reader sees two paragraphs and knows there are two
// kinds of gap, rather than one paragraph asserting something true of half of
// them.
type unwritableGroup struct {
	Reason  corpus.Reason
	Entries []corpus.Unwritable
}

// Features is the codes of a group, in the order the register holds them.
func (g unwritableGroup) Features() []string {
	out := make([]string, 0, len(g.Entries))
	for _, u := range g.Entries {
		out = append(out, u.Feature)
	}
	return out
}

// groupUnwritable splits a register by reason, keeping the reasons in the
// order they are first met so that a report is the same twice running.
func groupUnwritable(us []corpus.Unwritable) []unwritableGroup {
	reasons := corpus.Reasons(us)
	out := make([]unwritableGroup, 0, len(reasons))
	for _, reason := range reasons {
		g := unwritableGroup{Reason: reason}
		for _, u := range us {
			if u.Reason == reason {
				g.Entries = append(g.Entries, u)
			}
		}
		out = append(out, g)
	}
	return out
}

// codeList writes a handful of feature codes the way a sentence would.
func codeList(codes []string) string {
	switch len(codes) {
	case 0:
		return "none of them"
	case 1:
		return codes[0]
	case 2:
		return codes[0] + " and " + codes[1]
	}
	return strings.Join(codes[:len(codes)-1], ", ") + " and " + codes[len(codes)-1]
}
