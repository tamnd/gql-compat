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
