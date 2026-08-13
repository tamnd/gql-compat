package report_test

import (
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/grammar"
	"github.com/tamnd/gql-compat/metrics"
	"github.com/tamnd/gql-compat/report"
	"github.com/tamnd/gql-compat/runner"
)

// The grammar walk is the one part of a run that produces no result, and every
// format has to say so in its own way. What is checked here is that the
// statements are present, that they are marked as what they are, and that no
// number a reader or a CI gate takes for a score moved because the walk ran.

const reducedLead = "SESSION SET TIME ZONE 's'"

// walkedReport is the sample report with a walk attached.
func walkedReport() *runner.Report {
	rep := sample()
	rep.Exploration = &runner.Exploration{
		Seed: 4242, Start: grammar.DefaultStart, MaxDepth: 40,
		Walked: 12, Distinct: 10, Known: 1,
		Coverage: grammar.Coverage{
			Total: 814, Reachable: 699, Cut: 14,
			Unwritable: []string{"SQL-datetime literal"}, Start: grammar.DefaultStart,
		},
		Totals: runner.KindTotals{Cases: 2, Pass: 1, Fail: 1},
		Cases: []runner.CaseResult{
			{
				ID: "gen-0000000000001092-0000", Name: "a statement the published grammar admits",
				Kind: corpus.KindGenerated, Mode: runner.ModeConformance,
				Statement: "SESSION RESET ALL PARAMETERS",
				Outcome:   runner.Pass, Evidence: runner.EvidenceAccepted,
				Stats:   metrics.Stats{Count: 1, Min: ms(1), P50: ms(1), Max: ms(1), Mean: ms(1)},
				Repeats: 1, Wall: ms(1),
			},
			{
				ID: "gen-0000000000001092-0003", Name: "a statement the published grammar admits",
				Kind: corpus.KindGenerated, Mode: runner.ModeConformance,
				Statement: reducedLead + " SESSION RESET",
				Outcome:   runner.Fail, Evidence: runner.EvidenceStatus,
				Reason:    "the engine reports invalid syntax for a statement the published grammar admits",
				GotStatus: runner.StatusSyntaxError,
				Message:   "syntax error at or near <TIME>",
				Stats:     metrics.Stats{Count: 1, Min: ms(2), P50: ms(2), Max: ms(2), Mean: ms(2)},
				Repeats:   1, Wall: ms(2),
			},
		},
		Leads: []runner.Lead{{
			ID:          "gen-0000000000001092-0003",
			Statement:   reducedLead + " SESSION RESET",
			Reduced:     reducedLead,
			Path:        []string{"GQL-program", "session set command", "session set time zone clause"},
			GQLStatus:   runner.StatusSyntaxError,
			Message:     "syntax error at or near <TIME>",
			Tried:       6,
			Fingerprint: grammar.Fingerprint(reducedLead),
		}},
	}
	return rep
}

func renderReport(t *testing.T, rep *runner.Report, f report.Format) string {
	t.Helper()
	var b bytes.Buffer
	if err := report.Write(&b, rep, f); err != nil {
		t.Fatalf("rendering %s: %v", f, err)
	}
	return b.String()
}

func TestTheWalkGetsItsOwnSectionAndItsOwnRow(t *testing.T) {
	md := renderReport(t, walkedReport(), report.FormatMarkdown)
	if !strings.Contains(md, report.ExplorationHeading) {
		t.Fatal("the markdown report has no section for the walk")
	}
	// The row has to be under the total and outside it, and the rate has to be
	// a dash: a percentage there would be read as a conformance score.
	total := strings.Index(md, "| **total** |")
	row := strings.Index(md, "| _generated_ |")
	if total < 0 || row < 0 {
		t.Fatalf("scoreboard rows missing: total at %d, generated at %d", total, row)
	}
	if row < total {
		t.Error("the generated row is above the total, where it reads as part of it")
	}
	line := md[row : strings.Index(md[row:], "\n")+row]
	if !strings.Contains(line, "—") {
		t.Errorf("the generated row prints a pass rate: %s", line)
	}
	for _, want := range []string{reducedLead, "42001", "session set time zone clause", "4242"} {
		if !strings.Contains(md, want) {
			t.Errorf("the section does not print %q, which a reader needs to reproduce the lead", want)
		}
	}
	if !strings.Contains(md, grammar.Fingerprint(reducedLead)) {
		t.Error("the lead has no fingerprint, so there is no way to record that review dealt with it")
	}
}

func TestAReportWithNoWalkPrintsNoSection(t *testing.T) {
	md := renderReport(t, sample(), report.FormatMarkdown)
	if strings.Contains(md, report.ExplorationHeading) {
		t.Error("a run that never walked the grammar printed the section anyway")
	}
	if strings.Contains(md, "| _generated_ |") {
		t.Error("a run that never walked the grammar printed a generated scoreboard row")
	}
	if strings.Contains(md, "Generated statements are leads") {
		t.Error("the methodology explains a section the report does not have")
	}
}

// A walked statement that an engine rejected is a question. A CI gate that
// turned red on it would be gating a build on a question, which is exactly what
// the milestone said not to do.
func TestTheWalkChangesNoVerdictAnyGateReads(t *testing.T) {
	with, without := walkedReport(), sample()

	var a, b bytes.Buffer
	if err := report.WriteJUnit(&a, with); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteJUnit(&b, without); err != nil {
		t.Fatal(err)
	}
	var got, base report.JUnit
	if err := xml.Unmarshal(a.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal(b.Bytes(), &base); err != nil {
		t.Fatal(err)
	}
	if got.Failures != base.Failures || got.Errors != base.Errors {
		t.Errorf("the walk moved the gate: %d failures and %d errors became %d and %d",
			base.Failures, base.Errors, got.Failures, got.Errors)
	}
	if got.Skipped != base.Skipped+2 {
		t.Errorf("%d skipped, want the %d of the corpus plus the walk's two", got.Skipped, base.Skipped)
	}

	var suite *report.JUnitCase
	for i := range got.Suites {
		if got.Suites[i].Name == string(corpus.KindGenerated) {
			suite = &got.Suites[i]
		}
	}
	if suite == nil {
		t.Fatal("the walk is not in the CI artifact at all")
	}
	if suite.Failures != 0 {
		t.Errorf("%d of the walk's statements are failures", suite.Failures)
	}
	if suite.Skipped != suite.Tests {
		t.Errorf("%d of %d statements are skipped, want all of them", suite.Skipped, suite.Tests)
	}
	var lead *report.JUnitTest
	for i := range suite.Cases {
		if suite.Cases[i].Name == "gen-0000000000001092-0003" {
			lead = &suite.Cases[i]
		}
	}
	if lead == nil {
		t.Fatal("the lead is not in the CI artifact")
	}
	if lead.Failure != nil {
		t.Error("the lead is recorded as a failure")
	}
	if !strings.Contains(lead.Skipped.Message, "lead") {
		t.Errorf("the skip message does not call it a lead: %q", lead.Skipped.Message)
	}
	if !strings.Contains(lead.SystemOut, reducedLead) {
		t.Error("the CI artifact does not carry the reduced statement")
	}
}

// The CSV is the metric archive, so a statement that ran on the engine belongs
// in it. The kind column is what keeps a script from averaging it into a score.
func TestTheCSVCarriesTheWalkAndLabelsIt(t *testing.T) {
	var b bytes.Buffer
	if err := report.WriteCSV(&b, walkedReport()); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(&b).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v", err)
	}
	head, rows := records[0], records[1:]
	kind, id := -1, -1
	for i, name := range head {
		switch name {
		case "kind":
			kind = i
		case "id":
			id = i
		}
	}
	if kind < 0 || id < 0 {
		t.Fatal("the CSV has no kind or id column")
	}
	generated := 0
	for _, r := range rows {
		if r[kind] == string(corpus.KindGenerated) {
			generated++
			if !strings.HasPrefix(r[id], "gen-") {
				t.Errorf("row %s is labelled generated", r[id])
			}
		}
	}
	if generated != 2 {
		t.Errorf("%d generated rows, want the walk's 2", generated)
	}
	if len(rows) != len(sample().Cases)+2 {
		t.Errorf("%d rows, want the corpus's %d plus the walk's 2", len(rows), len(sample().Cases))
	}
}

func TestTheJSONArchiveKeepsTheLead(t *testing.T) {
	var j bytes.Buffer
	if err := report.WriteJSON(&j, walkedReport()); err != nil {
		t.Fatal(err)
	}
	back, err := report.ReadJSON(&j)
	if err != nil {
		t.Fatal(err)
	}
	if back.Exploration == nil {
		t.Fatal("the walk did not survive the round trip")
	}
	if len(back.Exploration.Leads) != 1 {
		t.Fatalf("%d leads came back, want 1", len(back.Exploration.Leads))
	}
	l := back.Exploration.Leads[0]
	if l.Reduced != reducedLead || len(l.Path) != 3 {
		t.Errorf("the lead came back as %q through %v", l.Reduced, l.Path)
	}
	if back.Exploration.Coverage.Total != 814 {
		t.Error("the coverage the walk reported did not survive")
	}
	// The scored cases and the walk's cases must still be two different
	// fields, because that separation is the only thing keeping a generated
	// statement out of a total.
	for i := range back.Cases {
		if back.Cases[i].Kind == corpus.KindGenerated {
			t.Fatalf("%s came back as a scored case", back.Cases[i].ID)
		}
	}
}

func TestTheHTMLReportSaysWhatTheWalkIsAndEscapesIt(t *testing.T) {
	html := renderReport(t, walkedReport(), report.FormatHTML)
	if !strings.Contains(html, `id="exploration"`) {
		t.Fatal("the HTML report has no section for the walk")
	}
	if !strings.Contains(html, "&#39;s&#39;") {
		t.Error("the HTML did not escape the statement it printed")
	}
	if !strings.Contains(html, `class="aside"`) {
		t.Error("the generated scoreboard row is not set apart from the rows it is not part of")
	}
	if !strings.Contains(html, "session set time zone clause") {
		t.Error("the HTML lead does not name the productions in dispute")
	}
}
