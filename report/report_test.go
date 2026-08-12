package report_test

import (
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/metrics"
	"github.com/tamnd/gql-compat/report"
	"github.com/tamnd/gql-compat/rows"
	"github.com/tamnd/gql-compat/runner"
)

// The rule this package exists to keep is in its own doc comment: a metric
// that was not available renders as unavailable, never as zero. Most of what
// follows is that rule, checked format by format, because the four derived
// views are where a real measurement and a missing one are easiest to confuse.

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// sample is a report with one case of every outcome, one case whose metrics
// were all obtainable, and one whose were not.
func sample() *runner.Report {
	measured := metrics.ProcessDelta{
		CPUUser: ms(40), CPUSys: ms(10), CPUOK: true,
		RSSStart: 1 << 20, RSSEnd: 2 << 20, RSSPeak: 3 << 20, MemoryOK: true,
		ReadBytes: 4096, WriteBytes: 8192, ReadOps: 4, WriteOps: 8, IOOK: true,
		MinorFaults: 120, MajorFaults: 3, FaultsOK: true,
		VoluntaryCS: 9, InvoluntaryCS: 1, CtxOK: true,
		Samples: 40,
	}
	// Everything false: a server engine on the far end of a socket, whose
	// process this machine cannot read and whose storage is not here.
	unmeasured := metrics.ProcessDelta{Samples: 0}

	stats := func(p50 time.Duration) metrics.Stats {
		return metrics.Stats{
			Count: 7, Min: p50 - ms(1), P50: p50, P90: p50 + ms(1), P95: p50 + ms(2),
			P99: p50 + ms(3), Max: p50 + ms(4), Mean: p50, StdDev: ms(1), MAD: ms(1),
			Total: 7 * p50, QueriesPerSec: 100, RowsPerSec: 200, CellsPerSec: 400,
			MeanRows: 2, MeanBytes: 64,
		}
	}

	load := &metrics.Load{Wall: ms(120), Nodes: 1000, Edges: 4000, Process: measured,
		Disk: metrics.DiskDelta{BytesAfter: 1 << 20, AllocAfter: 1 << 20, Files: 3, OK: true}}
	load.Compute()

	cases := []runner.CaseResult{
		{
			ID: "mandatory/match/basic", Name: "a bare node pattern", Kind: corpus.KindMandatory,
			Subclauses: []string{"14.9"}, Productions: []string{"<match statement>"},
			Fixture: "two", Mode: runner.ModeConformance,
			Statement: "MATCH (n) RETURN n.name ORDER BY n.name",
			Outcome:   runner.Pass, Evidence: runner.EvidenceRows,
			Stats:   stats(ms(5)),
			Process: measured,
			Disk:    metrics.DiskDelta{BytesBefore: 1 << 20, BytesAfter: 1 << 20, Files: 3, OK: true},
			Load:    load,
			Repeats: 7, Warmups: 1, Wall: ms(60),
		},
		{
			ID: "optional/gq13/limit", Name: "LIMIT", Kind: corpus.KindOptional,
			Features: []string{"GQ13"}, Mode: runner.ModeConformance,
			Statement: "MATCH (n) RETURN n LIMIT 1",
			Outcome:   runner.Fail, Evidence: runner.EvidenceRows,
			Reason: "the engine returned a different table",
			Diff:   &rows.Diff{Reason: "row count differs", Row: -1, Want: "1", Got: "2"},
			// The unavailable half: no process reading, no disk on this host.
			Stats: stats(ms(9)), Process: unmeasured, Disk: metrics.DiskDelta{},
			Repeats: 7, Warmups: 1, Wall: ms(90),
		},
		{
			ID: "condition/22012/divide", Name: "division by zero", Kind: corpus.KindCondition,
			Conditions: []string{"22012"}, Mode: runner.ModeConformance,
			Statement: "RETURN 1/0", Outcome: runner.Pass, Evidence: runner.EvidenceMessage,
			Message: "divide by zero", GotStatus: "", WantStatus: "22012",
			Stats: stats(ms(1)), Process: measured, Repeats: 7, Warmups: 1, Wall: ms(20),
		},
		{
			ID: "mandatory/temporal/date", Name: "DATE literal", Kind: corpus.KindMandatory,
			Mode: runner.ModeConformance, Outcome: runner.Skip, Skip: runner.SkipCapability,
			Reason:  "the engine cannot hold temporal values",
			Missing: []fixture.Capability{fixture.CapTemporalValues},
			Fixture: "dated",
		},
		{
			ID: "performance/scan/wide", Name: "full scan", Kind: corpus.KindPerformance,
			Mode: runner.ModeConformance, Statement: "MATCH (n) RETURN count(*)",
			Outcome: runner.Error, Reason: "the statement timed out after 30s",
			Repeats: 7, Warmups: 1, Wall: 30 * time.Second,
		},
	}

	rep := &runner.Report{
		Tool: "gql-compat", Schema: runner.ReportSchema,
		Generated: time.Unix(1_700_000_000, 0).UTC(),
		Engine: runner.EngineInfo{
			Adapter: "fake", Version: "0.1.0",
			Capabilities:     adapter.Capabilities{Isolated: true},
			DataCapabilities: []fixture.Capability{fixture.CapLabels, fixture.CapEdgeTypes},
		},
		Host: runner.HostInfo{OS: "linux", Arch: "arm64", CPUCores: 8, CPULogical: 16,
			GoVersion: "go1.26.5", GOMAXPROCS: 16},
		Run: runner.RunInfo{
			Mode: runner.ModeConformance, Repeats: 7, Warmups: 1,
			Timeout: 30 * time.Second, SampleInterval: 5 * time.Millisecond,
			Started:  time.Unix(1_700_000_000, 0).UTC(),
			Finished: time.Unix(1_700_000_060, 0).UTC(),
			Wall:     time.Minute, ISOSource: "iso/artifacts",
		},
		Cases: cases,
		Totals: runner.Totals{
			Cases: 5, Pass: 2, Fail: 1, Skip: 1, Error: 1, WeakEvidence: 1,
			ByKind: map[corpus.Kind]runner.KindTotals{
				corpus.KindMandatory:   {Cases: 2, Pass: 1, Skip: 1},
				corpus.KindOptional:    {Cases: 1, Fail: 1},
				corpus.KindCondition:   {Cases: 1, Pass: 1},
				corpus.KindPerformance: {Cases: 1, Error: 1},
			},
			BySkip: map[runner.SkipReason]int{runner.SkipCapability: 1},
		},
		Coverage: runner.Coverage{
			Features:      map[string]runner.Status{"GQ13": {Cases: 1, Fail: 1, Description: "LIMIT"}},
			Subclauses:    map[string]runner.Status{"14.9": {Cases: 1, Pass: 1, Description: "Match statement"}},
			Conditions:    map[string]runner.Status{"22012": {Cases: 1, Pass: 1, Description: "division by zero"}},
			Productions:   map[string]runner.Status{"<match statement>": {Cases: 1, Pass: 1}},
			Families:      []runner.FamilyCoverage{{Family: "GQ", Total: 20, Tested: 1, Supported: 0}},
			FeaturesTotal: 228, ConditionsTotal: 68, ProductionsTotal: 814, SubclausesTotal: 317,
		},
	}
	return rep
}

func render(t *testing.T, f report.Format) string {
	t.Helper()
	var b bytes.Buffer
	if err := report.Write(&b, sample(), f); err != nil {
		t.Fatalf("rendering %s: %v", f, err)
	}
	if b.Len() == 0 {
		t.Fatalf("%s rendered nothing", f)
	}
	return b.String()
}

func TestEveryFormatRenders(t *testing.T) {
	for _, f := range report.AllFormats {
		t.Run(string(f), func(t *testing.T) { render(t, f) })
	}
}

func TestUnknownFormatIsAnErrorAndNotAnEmptyFile(t *testing.T) {
	var b bytes.Buffer
	if err := report.Write(&b, sample(), report.Format("pdf")); err == nil {
		t.Error("an unknown format wrote nothing and reported success")
	}
}

func TestParseFormatAcceptsTheAliasesTheHelpTextPromises(t *testing.T) {
	for in, want := range map[string]report.Format{
		"json": report.FormatJSON, "md": report.FormatMarkdown,
		"markdown": report.FormatMarkdown, " HTML ": report.FormatHTML,
		"csv": report.FormatCSV, "junit": report.FormatJUnit, "xml": report.FormatJUnit,
	} {
		got, err := report.ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := report.ParseFormat("yaml"); err == nil {
		t.Error("an unknown format name was accepted")
	} else if !strings.Contains(err.Error(), "json") {
		t.Errorf("the error %q should list the formats that do exist", err)
	}
	for _, f := range report.AllFormats {
		if !strings.HasPrefix(f.Extension(), ".") {
			t.Errorf("%s has extension %q", f, f.Extension())
		}
	}
}

func TestJSONRoundTripsWithoutLosingAMeasurement(t *testing.T) {
	var b bytes.Buffer
	orig := sample()
	if err := report.WriteJSON(&b, orig); err != nil {
		t.Fatal(err)
	}
	back, err := report.ReadJSON(&b)
	if err != nil {
		t.Fatalf("the archive format could not read its own output: %v", err)
	}
	if back.Totals.Pass != orig.Totals.Pass || back.Totals.Skip != orig.Totals.Skip ||
		back.Totals.WeakEvidence != orig.Totals.WeakEvidence ||
		back.Totals.ByKind[corpus.KindMandatory] != orig.Totals.ByKind[corpus.KindMandatory] {
		t.Errorf("totals changed: %+v", back.Totals)
	}
	if len(back.Cases) != len(orig.Cases) {
		t.Fatalf("%d cases survived out of %d", len(back.Cases), len(orig.Cases))
	}
	first := back.Cases[0]
	if first.Stats.P99 != orig.Cases[0].Stats.P99 {
		t.Errorf("p99 came back as %v, want %v", first.Stats.P99, orig.Cases[0].Stats.P99)
	}
	if first.Load == nil || first.Load.BitsPerEdge != orig.Cases[0].Load.BitsPerEdge {
		t.Errorf("the load measurement did not survive: %+v", first.Load)
	}
	// The availability flags are the part a consumer must not lose: without
	// them a zero and an unmeasured field are the same JSON number.
	if !first.Process.CPUOK || back.Cases[1].Process.CPUOK {
		t.Error("the availability flags did not round-trip")
	}
	if back.Schema != runner.ReportSchema {
		t.Errorf("schema %d, want %d", back.Schema, runner.ReportSchema)
	}
}

func TestCSVLeavesUnavailableMetricsEmptyRatherThanZero(t *testing.T) {
	r := csv.NewReader(strings.NewReader(render(t, report.FormatCSV)))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("%d lines, want a header and five cases", len(records))
	}
	header := records[0]
	col := func(row []string, name string) string {
		for i, h := range header {
			if h == name {
				return row[i]
			}
		}
		t.Fatalf("no column %q", name)
		return ""
	}

	measured, unmeasured := records[1], records[2]
	if col(measured, "cpu_user_ns") == "" {
		t.Error("a CPU time that was read came out blank")
	}
	if col(measured, "disk_after_bytes") == "" {
		t.Error("a disk measurement that was taken came out blank")
	}
	for _, name := range []string{
		"cpu_user_ns", "rss_peak_bytes", "read_bytes", "minor_faults",
		"voluntary_ctx_switches", "disk_after_bytes", "disk_growth_bytes",
	} {
		if got := col(unmeasured, name); got != "" {
			t.Errorf("%s is %q for a case that measured nothing; a spreadsheet would average it in", name, got)
		}
	}
	// The column order is a contract with plotting scripts.
	if header[0] != "id" || header[4] != "outcome" {
		t.Errorf("the leading columns moved: %v", header[:5])
	}
}

func TestMarkdownSaysUnavailableWhereThereIsNoNumber(t *testing.T) {
	out := render(t, report.FormatMarkdown)

	if !strings.Contains(out, "GQL conformance report: fake") {
		t.Error("the report does not name the engine it is about")
	}
	// Two of five passed, but only three were judged: the headline must divide
	// by the judged cases, not by the case count.
	if !strings.Contains(out, "2 of 3 judged cases passed") {
		t.Error("the headline does not exclude skips and errors from the denominator")
	}
	if !strings.Contains(out, "—") {
		t.Error("nothing rendered as unavailable, though one case measured nothing")
	}
	// A failure a reader can act on names the case and the difference.
	if !strings.Contains(out, "optional/gq13/limit") || !strings.Contains(out, "row count differs") {
		t.Error("the failing case or its diff is missing from the report")
	}
	// A skip must be visible and explained, not quietly dropped.
	if !strings.Contains(out, "mandatory/temporal/date") || !strings.Contains(out, "temporal-values") {
		t.Error("the skipped case or its missing capability is not reported")
	}
	// Weak evidence is printed beside the score, never folded into it.
	if !strings.Contains(out, "GQLSTATUS") {
		t.Error("the message-only pass is not flagged as weak evidence")
	}
	// The ISO denominators, not the corpus's own.
	if !strings.Contains(out, "228") {
		t.Error("feature coverage is not stated against the ISO total")
	}
}

func TestMarkdownDoesNotLetAnEngineMessageBreakTheTable(t *testing.T) {
	rep := sample()
	// An engine is free to put a pipe, a newline, or an asterisk in its error
	// text. Any of them unescaped turns one table row into several.
	rep.Cases[1].Reason = "want a|b\ngot *c*"
	var b bytes.Buffer
	if err := report.WriteMarkdown(&b, rep); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "got") && strings.Contains(line, "|") {
			if strings.Contains(line, "a|b") {
				t.Errorf("an unescaped pipe from an engine message reached a table row: %q", line)
			}
		}
	}
	if strings.Contains(out, "want a|b\ngot") {
		t.Error("a newline in an engine message was passed through into a table cell")
	}
}

func TestHTMLEscapesWhateverTheEngineSaid(t *testing.T) {
	rep := sample()
	rep.Cases[1].Message = `<script>alert("x")</script>`
	rep.Cases[1].Statement = `RETURN "<b>" AS x`
	var b bytes.Buffer
	if err := report.WriteHTML(&b, rep); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "<script>alert") {
		t.Error("an engine's error text was written into the page as markup")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("the error text is neither escaped nor present; it was dropped")
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "<!") {
		t.Error("the HTML report has no doctype")
	}
}

func TestJUnitCountsSkipsAsSkippedAndErrorsAsErrors(t *testing.T) {
	var root report.JUnit
	if err := xml.Unmarshal([]byte(render(t, report.FormatJUnit)), &root); err != nil {
		t.Fatalf("the JUnit output is not well-formed XML: %v", err)
	}
	if root.Tests != 5 {
		t.Errorf("tests %d, want 5", root.Tests)
	}
	if root.Failures != 1 {
		t.Errorf("failures %d, want 1", root.Failures)
	}
	if root.Skipped != 1 {
		t.Errorf("skipped %d, want 1; a capability skip shown as a pass would be the opposite of the finding", root.Skipped)
	}
	if root.Errors != 1 {
		t.Errorf("errors %d, want 1", root.Errors)
	}
	// Grouping by kind is what makes a CI summary say "mandatory: 1 skipped".
	if len(root.Suites) != 4 {
		t.Errorf("%d suites, want one per kind present", len(root.Suites))
	}
	var found bool
	for _, s := range root.Suites {
		for _, c := range s.Cases {
			if strings.Contains(c.Name, "optional/gq13/limit") || strings.Contains(c.Classname+c.Name, "gq13") {
				found = true
				if c.Failure == nil {
					t.Error("the failing case carries no <failure> element")
				} else if c.Failure.Message == "" {
					t.Error("the <failure> has no message, so a red build says nothing")
				}
			}
		}
	}
	if !found {
		t.Error("the failing case is not in the XML at all")
	}
}

func TestAReportWithNoJudgedCasesDoesNotClaimAPerfectScore(t *testing.T) {
	// Every case skipped is the shape a wrong selector or a very limited
	// engine produces. Zero of zero must not render as 100%.
	rep := sample()
	for i := range rep.Cases {
		rep.Cases[i].Outcome = runner.Skip
		rep.Cases[i].Skip = runner.SkipCapability
	}
	rep.Totals = runner.Totals{Cases: 5, Skip: 5, ByKind: map[corpus.Kind]runner.KindTotals{
		corpus.KindMandatory: {Cases: 5, Skip: 5},
	}}
	var b bytes.Buffer
	if err := report.WriteMarkdown(&b, rep); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "100.0%") {
		t.Error("a run in which nothing was judged reported a perfect score")
	}
	if !strings.Contains(out, "no case produced a verdict") {
		t.Error("a run with no verdicts does not say so")
	}
}
